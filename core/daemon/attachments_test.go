// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// attachHelper is the workspace's helper for attachments: the agent's
// files to read (or the refusal its read gives), the agents' homes, and
// the panes a dropped path is offered to.
type attachHelper struct {
	*fakeTeamHelper

	amu        sync.Mutex
	files      map[string][]byte // box path → content
	refuse     map[string]string // box path → the helper's refusal word
	used       map[string]int64  // agent → its home's bytes
	homes      map[string]map[string][]byte
	writes     []*agentv1.WriteHomeFilesRequest
	inserted   []*agentv1.InsertDroppedPathRequest
	insertHow  typesv1.InsertHow
	failAppend bool
	reads      []string
}

func newAttachHelper() *attachHelper {
	return &attachHelper{fakeTeamHelper: &fakeTeamHelper{}, files: map[string][]byte{}, refuse: map[string]string{},
		used: map[string]int64{}, homes: map[string]map[string][]byte{}, insertHow: typesv1.InsertHow_INSERT_HOW_TYPED}
}

func (h *attachHelper) AppendBoard(ctx context.Context, in *agentv1.AppendBoardRequest, opts ...grpc.CallOption) (*agentv1.AppendBoardResponse, error) {
	h.amu.Lock()
	fail := h.failAppend
	h.amu.Unlock()
	if fail {
		return nil, errors.New("disk full")
	}
	return h.fakeTeamHelper.AppendBoard(ctx, in, opts...)
}

type sourceClient struct {
	agentv1.AgentService_ReadAttachmentSourceClient
	msgs []*agentv1.ReadAttachmentSourceResponse
	err  error
}

func (c *sourceClient) Recv() (*agentv1.ReadAttachmentSourceResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if len(c.msgs) == 0 {
		return nil, io.EOF
	}
	m := c.msgs[0]
	c.msgs = c.msgs[1:]
	return m, nil
}

func (h *attachHelper) ReadAttachmentSource(_ context.Context, in *agentv1.ReadAttachmentSourceRequest, _ ...grpc.CallOption) (agentv1.AgentService_ReadAttachmentSourceClient, error) {
	h.amu.Lock()
	defer h.amu.Unlock()
	h.reads = append(h.reads, in.GetPath())
	if !strings.HasPrefix(in.GetPath(), "/home/"+in.GetAgentId()+"/") {
		return &sourceClient{err: status.Error(codes.FailedPrecondition, "attach refused: outside")}, nil
	}
	if r, ok := h.refuse[in.GetPath()]; ok {
		return &sourceClient{err: status.Error(codes.FailedPrecondition, "attach refused: "+r)}, nil
	}
	b, ok := h.files[in.GetPath()]
	if !ok {
		return &sourceClient{err: status.Error(codes.FailedPrecondition, "attach refused: missing")}, nil
	}
	if int64(len(b)) > in.GetMaxBytes() {
		return &sourceClient{err: status.Error(codes.FailedPrecondition, fmt.Sprintf("attach refused: too-large %d", len(b)))}, nil
	}
	sum := sha256.Sum256(b)
	msgs := []*agentv1.ReadAttachmentSourceResponse{{Size: int64(len(b))}}
	for c := range slices.Chunk(b, 1000) {
		msgs = append(msgs, &agentv1.ReadAttachmentSourceResponse{Data: c})
	}
	msgs = append(msgs, &agentv1.ReadAttachmentSourceResponse{Sha256: hex.EncodeToString(sum[:])})
	return &sourceClient{msgs: msgs}, nil
}

func (h *attachHelper) ListHomeFiles(_ context.Context, in *agentv1.ListHomeFilesRequest, _ ...grpc.CallOption) (*agentv1.ListHomeFilesResponse, error) {
	h.amu.Lock()
	defer h.amu.Unlock()
	var files []*typesv1.AgentFile
	for p, b := range h.homes[in.GetAgentId()] {
		if path.Dir(p) == in.GetDir() {
			files = append(files, &typesv1.AgentFile{Path: p, Size: int64(len(b))})
		}
	}
	if in.GetDir() != "" && len(files) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "no such folder")
	}
	return &agentv1.ListHomeFilesResponse{Files: files, HomeBytes: h.used[in.GetAgentId()]}, nil
}

type attachWriteClient struct {
	agentv1.AgentService_WriteHomeFilesClient
	h     *attachHelper
	agent string
	quota int64
	path  string
	buf   []byte
	added []*typesv1.AgentFile
	err   error
}

func (w *attachWriteClient) flush() {
	if w.path == "" || w.err != nil {
		return
	}
	h := w.h
	if h.used[w.agent]+int64(len(w.buf)) > w.quota {
		w.err = status.Error(codes.ResourceExhausted, "the agent's files are full")
		return
	}
	if h.homes[w.agent] == nil {
		h.homes[w.agent] = map[string][]byte{}
	}
	if _, ok := h.homes[w.agent][w.path]; ok {
		w.err = status.Errorf(codes.FailedPrecondition, "%s is already there", w.path)
		return
	}
	h.homes[w.agent][w.path] = w.buf
	h.used[w.agent] += int64(len(w.buf))
	w.added = append(w.added, &typesv1.AgentFile{Path: w.path, Size: int64(len(w.buf))})
	w.path, w.buf = "", nil
}

func (w *attachWriteClient) Send(m *agentv1.WriteHomeFilesRequest) error {
	h := w.h
	h.amu.Lock()
	defer h.amu.Unlock()
	h.writes = append(h.writes, m)
	if m.GetAgentId() != "" {
		w.agent, w.quota = m.GetAgentId(), m.GetHomeQuotaBytes()
	}
	if m.GetPath() != "" && m.GetPath() != w.path {
		w.flush()
		w.path = m.GetPath()
	}
	w.buf = append(w.buf, m.GetData()...)
	if m.GetLast() {
		w.flush()
	}
	return nil
}

func (w *attachWriteClient) CloseAndRecv() (*agentv1.WriteHomeFilesResponse, error) {
	w.h.amu.Lock()
	defer w.h.amu.Unlock()
	w.flush()
	if w.err != nil {
		return nil, w.err
	}
	return &agentv1.WriteHomeFilesResponse{Added: w.added}, nil
}

func (h *attachHelper) WriteHomeFiles(_ context.Context, _ ...grpc.CallOption) (agentv1.AgentService_WriteHomeFilesClient, error) {
	return &attachWriteClient{h: h}, nil
}

func (h *attachHelper) InsertDroppedPath(_ context.Context, in *agentv1.InsertDroppedPathRequest, _ ...grpc.CallOption) (*agentv1.InsertDroppedPathResponse, error) {
	h.amu.Lock()
	defer h.amu.Unlock()
	h.inserted = append(h.inserted, in)
	return &agentv1.InsertDroppedPathResponse{How: h.insertHow}, nil
}

// newAttachEngine is a team engine whose workspace's attachment store is a
// temporary folder.
func newAttachEngine(t *testing.T) (*teamEngine, *attachHelper, string) {
	t.Helper()
	e, _, clock := newTestEngine(t, defaultTeam()...)
	h := newAttachHelper()
	e.helper = func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil }
	e.now = clock.now
	store := filepath.Join(t.TempDir(), "Attachments", "box-1")
	e.attachDir = func(context.Context, string) (string, error) { return store, nil }
	return e, h, store
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeader is a PNG's signature and IHDR claiming w×h, with no pixels:
// refused from the header, it's never decoded.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func jpegOf(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifOf(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 5, 2), []color.Color{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpOf is a lossless WebP's header (VP8L), w×h.
func webpOf(w, h uint32) []byte {
	bits := (w - 1) | (h-1)<<14
	vp8l := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24), 0, 0, 0}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(vp8l)))
	b.WriteString("WEBPVP8L")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(vp8l)))
	b.Write(vp8l)
	return b.Bytes()
}

const pdfBytes = "%PDF-1.7\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"

type docxPart struct {
	name string
	data string
	// declared overrides the uncompressed size the central directory
	// claims (a bomb).
	declared uint64
}

const docxTypes = `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`

func docxOf(t *testing.T, parts ...docxPart) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, p := range parts {
		if p.declared > 0 {
			w, err := z.CreateRaw(&zip.FileHeader{Name: p.name, Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte(p.data)),
				CompressedSize64: uint64(len(p.data)), UncompressedSize64: p.declared})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(p.data))
			continue
		}
		w, err := z.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(p.data))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func goodDocx(t *testing.T, extra ...docxPart) []byte {
	return docxOf(t, append([]docxPart{{name: "[Content_Types].xml", data: docxTypes}, {name: "word/document.xml", data: "<w:document/>"}}, extra...)...)
}

func attachMsg(from, to, text string, paths ...string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: to, Text: text, Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE, AttachPaths: paths}}}
}

func attachAsk(from, text string, paths ...string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: text, AttachPaths: paths}}}
}

// storeEntries is the store's attachment folders (and anything else in it).
func storeEntries(t *testing.T, store string) []string {
	t.Helper()
	es, err := os.ReadDir(store)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// ADR 0030 point 2: an agent attaches a file from its home to a message
// for the owner. The board line carries the record; the bytes land in the
// store, 0700 folders and 0600 files; nothing is copied into any home.
func TestAnAgentAttachesToTheOwner(t *testing.T) {
	e, h, store := newAttachEngine(t)
	shot := pngOf(t, 390, 844)
	h.files["/home/builder/shots/login-390.png"] = shot
	r := e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "The login page is broken on narrow screens.", "/home/builder/shots/login-390.png"))
	if !r.GetOk() {
		t.Fatalf("refused: %s", r.GetLine())
	}
	m := r.GetMessage()
	if len(m.GetAttachments()) != 1 {
		t.Fatalf("message %v", m)
	}
	a := m.GetAttachments()[0]
	sum := sha256.Sum256(shot)
	if !ulidRE.MatchString(a.GetId()) || a.GetName() != "login-390.png" || a.GetMediaType() != "image/png" || a.GetSize() != int64(len(shot)) ||
		a.GetSha256() != hex.EncodeToString(sum[:]) || a.GetWidth() != 390 || a.GetHeight() != 844 || a.GetInboxPath() != "" {
		t.Errorf("attachment %v", a)
	}
	dir := filepath.Join(store, a.GetId())
	got, err := os.ReadFile(filepath.Join(dir, "data"))
	if err != nil || !bytes.Equal(got, shot) {
		t.Fatalf("stored bytes: %v", err)
	}
	for p, want := range map[string]os.FileMode{store: 0o700, dir: 0o700, filepath.Join(dir, "data"): 0o600, filepath.Join(dir, "meta.json"): 0o600} {
		if got := mode(t, p); got != want {
			t.Errorf("%s: mode %o, want %o", p, got, want)
		}
	}
	var meta attachMeta
	b, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err := json.Unmarshal(b, &meta); err != nil || meta.ID != a.GetId() || meta.From != "builder" || !slices.Equal(meta.MessageIDs, []string{m.GetId()}) || meta.SHA256 != a.GetSha256() {
		t.Errorf("meta.json: %s, %v", b, err)
	}
	if len(h.writes) != 0 {
		t.Errorf("copied into a home: %v", h.writes)
	}
	// The agent's answer never names a Mac path.
	if txt := prototext.Format(r); strings.Contains(txt, store) || strings.Contains(txt, "Library") || strings.Contains(txt, "Attachments/") {
		t.Errorf("the answer names the store: %s", txt)
	}
	if lines := h.boardLines(); len(lines) != 1 || len(lines[0].GetMessage().GetAttachments()) != 1 {
		t.Errorf("board %v", lines)
	}
	// Attach-only: the text says what was attached.
	h.files["/home/builder/shop/docs/redesign.md"] = []byte("# Redesign\n")
	r = e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "", "/home/builder/shop/docs/redesign.md"))
	if !r.GetOk() || r.GetMessage().GetText() != "attached redesign.md" {
		t.Errorf("attach-only: %v", r)
	}
}

// Every row of ADR 0030 point 2's failure table, and the secrets checks:
// one plain line, nothing on the board, nothing left in the store.
func TestEveryAttachRefusalHasItsLine(t *testing.T) {
	big := bytes.Repeat([]byte("x"), teamkit.MaxAttachmentBytes+1)
	cases := []struct {
		name  string
		path  string
		data  []byte
		refus string
		want  string
	}{
		{"the project", "/home/work/w/README.md", []byte("x"), "", "Attach files from your own home (/home/builder), by their full path. Copy a file there first."},
		{"/home/team", "/home/team/notes.md", []byte("x"), "", "Attach files from your own home (/home/builder), by their full path. Copy a file there first."},
		{"another agent's clone", "/home/lead/w/plan.md", []byte("x"), "", "Attach files from your own home (/home/builder), by their full path. Copy a file there first."},
		{"a relative path", "shots/login.png", []byte("x"), "", "Attach files from your own home (/home/builder), by their full path. Copy a file there first."},
		{"a path with ..", "/home/builder/../lead/w/plan.md", []byte("x"), "", "Attach files from your own home (/home/builder), by their full path. Copy a file there first."},
		{"missing", "/home/builder/shots/login.png", nil, "missing", "There's no file at ~/shots/login.png."},
		{"a link", "/home/builder/shots/latest.png", nil, "link", "~/shots/latest.png is a link. Attach the file itself."},
		{"a folder", "/home/builder/shots", nil, "folder", "~/shots is a folder. Attach files one at a time."},
		{"a FIFO", "/home/builder/pipe.md", nil, "special", "~/pipe.md isn't a regular file. Attach the file itself."},
		{"not its own", "/home/builder/hard-plan.md", nil, "not-own", "~/hard-plan.md belongs to another user. Copy it into your home first."},
		{"too large", "/home/builder/screenshot.png", big, "", "screenshot.png is 20 MB; an attachment can be at most 20 MB. Make it smaller and attach it again."},
		{"an SVG", "/home/builder/logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "", "logo.svg isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"HTML", "/home/builder/page.html", []byte("<html><body>hi</body></html>"), "", "page.html isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"text named .pdf", "/home/builder/report.pdf", []byte("just text"), "", "report.pdf isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"binary named .md", "/home/builder/notes.md", []byte("bin\x00ary"), "", "notes.md isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"an executable", "/home/builder/tool.txt", []byte("\x7fELF\x02\x01\x01\x00"), "", "tool.txt isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"a docx from an agent", "/home/builder/spec.docx", goodDocx(t), "", "spec.docx isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"a PDF without its end", "/home/builder/cut.pdf", []byte("%PDF-1.7\n1 0 obj"), "", "cut.pdf isn't a PDF, PNG, JPEG, GIF, WebP, Markdown or text file. Attach one of those."},
		{"too wide", "/home/builder/login.png", pngHeader(40000, 10), "", "login.png is 40,000 px wide; images can be at most 16,384 px a side."},
		{"too tall", "/home/builder/tall.png", pngHeader(10, 20000), "", "tall.png is 20,000 px tall; images can be at most 16,384 px a side."},
		{"too many pixels", "/home/builder/huge.png", pngHeader(16000, 16000), "", "huge.png is 256 megapixels; images can be at most 100 megapixels."},
		{"a hidden folder", "/home/builder/.ssh/notes.txt", []byte("x"), "", teamkit.AttachSecretLine("~/.ssh/notes.txt")},
		{"the clone's .git", "/home/builder/w/.git/config", []byte("x"), "", teamkit.AttachSecretLine("~/w/.git/config")},
		{"a .env", "/home/builder/w/.env", []byte("A=1"), "", teamkit.AttachSecretLine("~/w/.env")},
		{"a .env.local", "/home/builder/w/.env.local", []byte("A=1"), "", teamkit.AttachSecretLine("~/w/.env.local")},
		{"a key block in notes.md", "/home/builder/notes.md", []byte("# Notes\n-----BEGIN OPENSSH " + "PRIVATE" + " KEY-----\nb3BlbnNzaA\n"), "", teamkit.AttachSecretLine("~/notes.md")},
		{"an AWS secret in a .txt", "/home/builder/env.txt", []byte("aws_secret_access_key = abc\n"), "", teamkit.AttachSecretLine("~/env.txt")},
	}
	for _, p := range teamkit.SecretNamePatterns {
		n := strings.ReplaceAll(p, "*", "x")
		cases = append(cases, struct {
			name, path string
			data       []byte
			refus      string
			want       string
		}{"deny-list " + p, "/home/builder/" + n, []byte("plain words"), "", teamkit.AttachSecretLine("~/" + n)})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, h, store := newAttachEngine(t)
			if c.data != nil {
				h.files[c.path] = c.data
			}
			if c.refus != "" {
				h.refuse[c.path] = c.refus
			}
			for _, call := range []*agentv1.TeamCallsResponse{attachMsg("builder", "owner", "look", c.path), attachMsg("builder", "lead", "look", c.path), attachAsk("builder", "Ship?", c.path)} {
				r := e.handleCall(context.Background(), "w", call)
				if r.GetOk() || r.GetLine() != c.want {
					t.Errorf("line %q, want %q", r.GetLine(), c.want)
				}
			}
			if n := len(h.boardLines()); n != 0 {
				t.Errorf("%d board lines", n)
			}
			if es := storeEntries(t, store); len(es) != 0 {
				t.Errorf("store holds %v", es)
			}
			if len(h.writes) != 0 {
				t.Errorf("a home was written: %v", h.writes)
			}
		})
	}
}

// Names are never the guard on their own: a key named notes.md is caught
// by its content, and a deny-listed name with harmless content by its
// name. The secrets checks run before the file is read.
func TestTheSecretChecksRunBeforeTheRead(t *testing.T) {
	e, h, _ := newAttachEngine(t)
	for _, p := range []string{"/home/builder/.ssh/id_ed25519", "/home/builder/deploy.pem", "/home/builder/.claude/creds.md"} {
		h.files[p] = []byte("x")
		if r := e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "look", p)); r.GetOk() {
			t.Errorf("%s attached", p)
		}
	}
	if len(h.reads) != 0 {
		t.Errorf("read before the name checks: %v", h.reads)
	}
}

// The type is the content's: a PNG named .pdf lands as .png; each allowed
// type passes with its media type.
func TestTheTypeComesFromTheContent(t *testing.T) {
	e, h, _ := newAttachEngine(t)
	for p, want := range map[string][3]string{
		"/home/builder/notes.pdf":   {"notes.png", "image/png", ""},
		"/home/builder/photo.jpg":   {"photo.jpg", "image/jpeg", ""},
		"/home/builder/anim.gif":    {"anim.gif", "image/gif", ""},
		"/home/builder/pic.webp":    {"pic.webp", "image/webp", ""},
		"/home/builder/report.pdf":  {"report.pdf", "application/pdf", ""},
		"/home/builder/plan.md":     {"plan.md", "text/markdown", ""},
		"/home/builder/log.txt":     {"log.txt", "text/plain", ""},
		"/home/builder/shot":        {"shot.png", "image/png", ""},
		"/home/builder/Plan.MD":     {"Plan.md", "text/markdown", ""},
		"/home/builder/photo.jpeg":  {"photo.jpg", "image/jpeg", ""},
		"/home/builder/crlf.txt":    {"crlf.txt", "text/plain", ""},
		"/home/builder/-rm -rf.png": {"rm -rf.png", "image/png", ""},
	} {
		switch path.Ext(want[0]) {
		case ".png":
			h.files[p] = pngOf(t, 3, 2)
		case ".jpg":
			h.files[p] = jpegOf(t)
		case ".gif":
			h.files[p] = gifOf(t)
		case ".webp":
			h.files[p] = webpOf(7, 5)
		case ".pdf":
			h.files[p] = []byte(pdfBytes)
		default:
			h.files[p] = []byte("line one\r\n\tline two\n")
		}
		r := e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "look", p))
		if !r.GetOk() {
			t.Errorf("%s: %s", p, r.GetLine())
			continue
		}
		a := r.GetMessage().GetAttachments()[0]
		if a.GetName() != want[0] || a.GetMediaType() != want[1] {
			t.Errorf("%s: %s %s, want %s %s", p, a.GetName(), a.GetMediaType(), want[0], want[1])
		}
	}
}

// ADR 0030 point 1's counts: 5 per message, 100 a day per agent, the
// workspace's store limit; each refuses rather than trims or evicts.
func TestAttachmentLimits(t *testing.T) {
	e, h, store := newAttachEngine(t)
	ctx := context.Background()
	var six []string
	for i := range 6 {
		p := fmt.Sprintf("/home/builder/n%d.txt", i)
		h.files[p] = []byte("note")
		six = append(six, p)
	}
	if r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", six...)); r.GetOk() || r.GetLine() != "Attach at most 5 files to one message or question." {
		t.Errorf("six: %v", r)
	}
	if r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", six[:5]...)); !r.GetOk() || len(r.GetMessage().GetAttachments()) != 5 {
		t.Fatalf("five: %v", r)
	}
	// The daily limit.
	e.attachPerDay = 7
	if r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", six[:3]...)); r.GetOk() || r.GetLine() != "builder has sent 5 attachments in the last 24 hours; its limit is 7. Describe the rest in a message." {
		t.Errorf("past the day's limit: %v", r)
	}
	if r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", six[:2]...)); !r.GetOk() {
		t.Errorf("up to the day's limit: %v", r)
	}
	who := e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	if who.GetAttachmentsToday() != 7 || who.GetAttachmentsPerDay() != 7 {
		t.Errorf("whoami: %d of %d", who.GetAttachmentsToday(), who.GetAttachmentsPerDay())
	}
	if r := e.handleCall(ctx, "w", attachMsg("lead", "owner", "look")); !r.GetOk() {
		t.Errorf("another agent's own count: %v", r)
	}
	// A day later, it can again.
	e.now = func() time.Time { return time.Date(2026, 10, 18, 9, 1, 0, 0, time.UTC) }
	before := len(storeEntries(t, store))
	// The store's limit.
	e.attachStoreLimit = 30
	h.files["/home/builder/more.txt"] = []byte("0123456789012345678901234567890123")
	if r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", "/home/builder/more.txt")); r.GetOk() || r.GetLine() != "This workspace's attachments use 28 bytes of 30 bytes. Remove some in the preview's … menu, or raise the limit in Settings." {
		t.Errorf("store full: %v", r)
	}
	if got := len(storeEntries(t, store)); got != before {
		t.Errorf("store went from %d to %d entries", before, got)
	}
}

// Agent to agent (ADR 0030 point 2): the recipient gets a copy in
// ~/inbox/<sender>/<time>/, as the recipient, with the checked bytes, and
// the record names it. A home too full for it refuses the whole message.
func TestAnAttachmentToATeammateLandsInItsInbox(t *testing.T) {
	e, h, store := newAttachEngine(t)
	ctx := context.Background()
	shot := pngOf(t, 4, 4)
	h.files["/home/builder/shots/a.png"] = shot
	h.files["/home/builder/shots/b.md"] = []byte("# B")
	r := e.handleCall(ctx, "w", attachMsg("builder", "lead", "both", "/home/builder/shots/a.png", "/home/builder/shots/b.md"))
	if !r.GetOk() {
		t.Fatalf("refused: %s", r.GetLine())
	}
	as := r.GetMessage().GetAttachments()
	want := []string{"inbox/builder/20261017T090000Z/a.png", "inbox/builder/20261017T090000Z/b.md"}
	for i, a := range as {
		if a.GetInboxPath() != want[i] {
			t.Errorf("inbox path %q, want %q", a.GetInboxPath(), want[i])
		}
		copied := h.homes["lead"][a.GetInboxPath()]
		sum := sha256.Sum256(copied)
		if hex.EncodeToString(sum[:]) != a.GetSha256() {
			t.Errorf("%s: the copy doesn't match its SHA-256", a.GetName())
		}
	}
	for _, w := range h.writes {
		if w.GetAgentId() != "" && w.GetAgentId() != "lead" {
			t.Errorf("written as %s", w.GetAgentId())
		}
	}
	if len(h.homes["builder"]) != 0 || len(h.homes["reviewer"]) != 0 {
		t.Errorf("other homes touched: %v", h.homes)
	}
	// The same name again in the same second: (2), never overwritten.
	r = e.handleCall(ctx, "w", attachMsg("builder", "lead", "again", "/home/builder/shots/a.png", "/home/builder/shots/a.png"))
	if !r.GetOk() {
		t.Fatalf("again: %s", r.GetLine())
	}
	got := []string{r.GetMessage().GetAttachments()[0].GetInboxPath(), r.GetMessage().GetAttachments()[1].GetInboxPath()}
	if !slices.Equal(got, []string{"inbox/builder/20261017T090000Z/a (2).png", "inbox/builder/20261017T090000Z/a (3).png"}) {
		t.Errorf("collisions: %v", got)
	}
	// A full home refuses the whole message: no copy, no line, no store.
	lines, entries := len(h.boardLines()), len(storeEntries(t, store))
	h.used["lead"] = teamkit.DefaultHomeQuota - 10
	r = e.handleCall(ctx, "w", attachMsg("builder", "lead", "more", "/home/builder/shots/a.png"))
	if r.GetOk() || r.GetLine() != "lead's files are full (2.0 GB of 2.0 GB), so the attachment wasn't sent. Ask the owner to make room." {
		t.Errorf("a full home: %v", r)
	}
	if len(h.boardLines()) != lines || len(storeEntries(t, store)) != entries {
		t.Error("a refused message left a line or a stored file")
	}
}

// The board line is written last: when it fails, the call removes what
// it stored, and nothing names an attachment that isn't there.
func TestTheBoardLineIsLast(t *testing.T) {
	e, h, store := newAttachEngine(t)
	h.files["/home/builder/a.png"] = pngOf(t, 2, 2)
	h.failAppend = true
	r := e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "look", "/home/builder/a.png"))
	if r.GetOk() {
		t.Fatal("written without its board line")
	}
	if es := storeEntries(t, store); len(es) != 0 {
		t.Errorf("store holds %v after the board failed", es)
	}
	h.failAppend = false
	r = e.handleCall(context.Background(), "w", attachAsk("builder", "Ship the redesign?", "/home/builder/a.png"))
	if !r.GetOk() || len(r.GetDecision().GetAttachments()) != 1 {
		t.Fatalf("ask-owner: %v", r)
	}
	var meta attachMeta
	b, _ := os.ReadFile(filepath.Join(store, r.GetDecision().GetAttachments()[0].GetId(), "meta.json"))
	if json.Unmarshal(b, &meta) != nil || !slices.Equal(meta.DecisionIDs, []string{r.GetDecision().GetId()}) {
		t.Errorf("meta %s", b)
	}
	// ask-owner keeps its grant.
	if r := e.handleCall(context.Background(), "w", attachAsk("reviewer", "Ship?", "/home/reviewer/a.png")); r.GetOk() || !strings.Contains(r.GetLine(), "may not ask the owner") {
		t.Errorf("reviewer: %v", r)
	}
}

// agentCtx and ownerCtx are a call's caller, as Serve attaches it.
func callerCtx(who caller) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{CommonAuthInfo: credentials.CommonAuthInfo{}, who: who}})
}

type readAttachmentStream struct {
	daemonv1.DaemonService_ReadAttachmentServer
	ctx  context.Context
	msgs []*daemonv1.ReadAttachmentResponse
}

func (s *readAttachmentStream) Context() context.Context { return s.ctx }
func (s *readAttachmentStream) Send(m *daemonv1.ReadAttachmentResponse) error {
	s.msgs = append(s.msgs, m)
	return nil
}

type sendAttachmentsStream struct {
	daemonv1.DaemonService_SendAttachmentsServer
	ctx context.Context
}

func (s *sendAttachmentsStream) Context() context.Context { return s.ctx }
func (s *sendAttachmentsStream) Recv() (*daemonv1.SendAttachmentsRequest, error) {
	return nil, io.EOF
}

// Only the person at this Mac reads, sends or removes attachments: an
// identified agent is refused with PermissionDenied, and a call with no
// caller with Unauthenticated, before anything is looked at.
func TestTheAppsAttachmentCallsAreTheOwnersOnly(t *testing.T) {
	s := &Server{}
	agent := callerCtx(caller{kind: callerAgent, id: "builder", name: "Builder", box: "w"})
	for name, c := range map[string]struct {
		ctx  context.Context
		want codes.Code
	}{"an agent": {agent, codes.PermissionDenied}, "no caller": {context.Background(), codes.Unauthenticated}} {
		if _, err := s.RemoveAttachment(c.ctx, &daemonv1.RemoveAttachmentRequest{Name: "w", AttachmentId: "01JAB000000000000000000000"}); status.Code(err) != c.want {
			t.Errorf("%s: RemoveAttachment: %v, want %v", name, err, c.want)
		}
		if err := s.ReadAttachment(&daemonv1.ReadAttachmentRequest{Name: "w", AttachmentId: "01JAB000000000000000000000"}, &readAttachmentStream{ctx: c.ctx}); status.Code(err) != c.want {
			t.Errorf("%s: ReadAttachment: %v, want %v", name, err, c.want)
		}
		if err := s.SendAttachments(&sendAttachmentsStream{ctx: c.ctx}); status.Code(err) != c.want {
			t.Errorf("%s: SendAttachments: %v, want %v", name, err, c.want)
		}
	}
}

// ReadAttachment streams the record, then the bytes; RemoveAttachment
// deletes the bytes and keeps the board's record.
func TestTheOwnerReadsAndRemovesAnAttachment(t *testing.T) {
	e, h, store := newAttachEngine(t)
	ctx := context.Background()
	shot := pngOf(t, 9, 9)
	h.files["/home/builder/a.png"] = shot
	r := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", "/home/builder/a.png"))
	id := r.GetMessage().GetAttachments()[0].GetId()
	var msgs []*daemonv1.ReadAttachmentResponse
	if err := e.readAttachment(ctx, "w", id, func(m *daemonv1.ReadAttachmentResponse) error { msgs = append(msgs, m); return nil }); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i, m := range msgs {
		if (i == 0) != (m.GetAttachment() != nil) {
			t.Errorf("message %d: the record comes first, and only then", i)
		}
		got = append(got, m.GetData()...)
	}
	if !bytes.Equal(got, shot) || msgs[0].GetAttachment().GetName() != "a.png" {
		t.Errorf("read back %d bytes, %v", len(got), msgs[0].GetAttachment())
	}
	for _, bad := range []string{"01JAB000000000000000000000", "../box-2/x", "", "01jab000000000000000000000"} {
		err := e.readAttachment(ctx, "w", bad, func(*daemonv1.ReadAttachmentResponse) error { return nil })
		if err == nil || err.Error() != "This attachment isn't on this Mac." {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := e.removeAttachment(ctx, "w", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("still there: %v", err)
	}
	if len(h.boardLines()) != 1 {
		t.Error("the record went with the bytes")
	}
	if err := e.readAttachment(ctx, "w", id, func(*daemonv1.ReadAttachmentResponse) error { return nil }); err == nil {
		t.Error("read after removal")
	}
}

// The store's folders are excluded from Time Machine, on a Mac.
func TestTheStoreIsExcludedFromBackups(t *testing.T) {
	e, h, store := newAttachEngine(t)
	h.files["/home/builder/a.txt"] = []byte("a")
	if r := e.handleCall(context.Background(), "w", attachMsg("builder", "owner", "look", "/home/builder/a.txt")); !r.GetOk() {
		t.Fatal(r.GetLine())
	}
	for _, d := range []string{filepath.Dir(store), store} {
		if !excludedFromBackup(d) {
			t.Errorf("%s isn't excluded from backups", d)
		}
	}
}

// ADR 0030 point 6: after a complete read of the board, attachments no
// line names are removed; a partial read sweeps nothing.
func TestTheSweepRunsOnlyAfterACompleteBoardRead(t *testing.T) {
	for _, complete := range []bool{false, true} {
		e, h, store := newAttachEngine(t)
		ctx := context.Background()
		h.files["/home/builder/a.png"] = pngOf(t, 2, 2)
		named := e.handleCall(ctx, "w", attachMsg("builder", "owner", "look", "/home/builder/a.png")).GetMessage().GetAttachments()[0].GetId()
		asked := e.handleCall(ctx, "w", attachAsk("builder", "Ship?", "/home/builder/a.png")).GetDecision().GetAttachments()[0].GetId()
		orphan, staleTmp, freshTmp := newULID(e.now()), ".tmp-"+newULID(e.now()), ".tmp-"+newULID(e.now())
		for _, d := range []string{orphan, staleTmp, freshTmp, "not-ours"} {
			if err := os.MkdirAll(filepath.Join(store, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(filepath.Join(store, staleTmp), old, old); err != nil {
			t.Fatal(err)
		}
		// A restarted portenvd reads the board back.
		e2, _, _ := newAttachEngine(t)
		e2.attachDir = e.attachDir
		e2.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) {
			lines := h.boardLines()
			if complete {
				lines = append(lines, &agentv1.ReadBoardResponse{Complete: true})
			}
			return lines, nil
		}
		b, err := e2.lockBox(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		b.mu.Unlock()
		left := storeEntries(t, store)
		if !slices.Contains(left, named) || !slices.Contains(left, asked) {
			t.Errorf("complete=%v: a named attachment went: %v", complete, left)
		}
		if complete == slices.Contains(left, orphan) || complete == slices.Contains(left, staleTmp) {
			t.Errorf("complete=%v: the orphan and the stale write: %v", complete, left)
		}
		if !slices.Contains(left, freshTmp) || !slices.Contains(left, "not-ours") {
			t.Errorf("complete=%v: a write in progress or a name that isn't the store's went: %v", complete, left)
		}
	}
	// A board that can't be read sweeps nothing either.
	e, _, store := newAttachEngine(t)
	orphan := newULID(e.now())
	_ = os.MkdirAll(filepath.Join(store, orphan), 0o700)
	e.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) {
		return nil, errors.New("too large")
	}
	if _, err := e.lockBox(context.Background(), "w"); err == nil {
		t.Fatal("an unreadable board loaded")
	}
	if !slices.Contains(storeEntries(t, store), orphan) {
		t.Error("swept from an unreadable board")
	}
}

// sendFiles is the app's SendAttachments stream: the header, then each
// file in pieces.
func sendFiles(name string, to []string, pane string, files ...[2]string) func() (*daemonv1.SendAttachmentsRequest, error) {
	msgs := []*daemonv1.SendAttachmentsRequest{{Name: name, To: to, PaneId: pane}}
	for _, f := range files {
		data := []byte(f[1])
		first := true
		for len(data) > 0 || first {
			n := min(len(data), 700)
			m := &daemonv1.SendAttachmentsRequest{Data: data[:n], LastOfFile: n == len(data)}
			if first {
				m.FileName, m.Size, first = f[0], int64(len(f[1])), false
			}
			msgs = append(msgs, m)
			data = data[n:]
		}
	}
	return func() (*daemonv1.SendAttachmentsRequest, error) {
		if len(msgs) == 0 {
			return nil, io.EOF
		}
		m := msgs[0]
		msgs = msgs[1:]
		return m, nil
	}
}

// ADR 0030 point 8: the owner's file is stored once, copied into each
// chosen agent's ~/inbox/owner/<time>/ and nowhere else, with one notice
// per agent, and no wake-up.
func TestTheOwnerSendsAFileToAgents(t *testing.T) {
	e, h, store := newAttachEngine(t)
	ctx := context.Background()
	r, err := e.sendAttachments(ctx, sendFiles("w", []string{"lead", "builder"}, "", [2]string{"SPEC.md", "# Spec\nBuild it.\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetMessages()) != 2 || len(r.GetRefused()) != 0 {
		t.Fatalf("result %v", r)
	}
	if es := storeEntries(t, store); len(es) != 1 {
		t.Errorf("stored %v, want once", es)
	}
	for _, m := range r.GetMessages() {
		a := m.GetAttachments()[0]
		if m.GetFrom() != "owner" || m.GetType() != typesv1.MessageType_MESSAGE_TYPE_NOTICE || m.GetText() != "attached SPEC.md" ||
			a.GetInboxPath() != "inbox/owner/20261017T090000Z/SPEC.md" || string(h.homes[m.GetTo()][a.GetInboxPath()]) != "# Spec\nBuild it.\n" {
			t.Errorf("message %v", m)
		}
	}
	if len(h.homes["reviewer"]) != 0 {
		t.Error("reviewer's home touched")
	}
	b, _ := e.lockBox(ctx, "w")
	wakes := len(b.wakes)
	for _, w := range b.wakes {
		if w.count == 0 {
			wakes--
		}
	}
	b.mu.Unlock()
	if wakes != 0 {
		t.Errorf("an attachment woke %d agents", wakes)
	}
	var meta attachMeta
	mb, _ := os.ReadFile(filepath.Join(store, r.GetMessages()[0].GetAttachments()[0].GetId(), "meta.json"))
	if json.Unmarshal(mb, &meta) != nil || len(meta.MessageIDs) != 2 || meta.From != "owner" {
		t.Errorf("meta %s", mb)
	}
	// Several files: "attached 2 files".
	r, err = e.sendAttachments(ctx, sendFiles("w", []string{"lead"}, "", [2]string{"a.txt", "a"}, [2]string{"b.txt", "b"}))
	if err != nil || len(r.GetMessages()) != 1 || r.GetMessages()[0].GetText() != "attached 2 files" {
		t.Errorf("two files: %v, %v", r, err)
	}
}

// One agent's full home refuses only its copy, with its line.
func TestOneFullHomeRefusesOnlyItsCopy(t *testing.T) {
	e, h, _ := newAttachEngine(t)
	h.used["reviewer"] = teamkit.DefaultHomeQuota
	r, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"lead", "reviewer"}, "", [2]string{"SPEC.md", "# Spec"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetMessages()) != 1 || r.GetMessages()[0].GetTo() != "lead" ||
		!slices.Equal(r.GetRefused(), []string{"Reviewer's files are full (2.0 GB of 2.0 GB), so it didn't get SPEC.md. Remove some in Agent files, or raise its limit in Settings."}) {
		t.Errorf("result %v", r)
	}
}

// The owner's files get the same checks, and .docx's own (point 4).
func TestTheOwnersFilesAreChecked(t *testing.T) {
	cases := []struct {
		name, file, data string
		want             string
	}{
		{"a folder name", "", "x", "A file needs a name."},
		{"an SVG", "logo.svg", "<svg/>", "logo.svg isn't a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."},
		{"a ZIP bomb", "big.docx", string(goodDocx(t, docxPart{name: "word/media/huge.bin", data: "x", declared: 300 << 20})), "big.docx isn't a Word document Portenv can pass on safely (it unpacks to more than 200 MB)."},
		{"a ../ entry", "trav.docx", string(goodDocx(t, docxPart{name: "../../.bashrc", data: "x"})), "trav.docx isn't a Word document Portenv can pass on safely (a part is named outside the document)."},
		{"an absolute entry", "abs.docx", string(goodDocx(t, docxPart{name: "/etc/passwd", data: "x"})), "abs.docx isn't a Word document Portenv can pass on safely (a part is named outside the document)."},
		{"a backslash entry", "bs.docx", string(goodDocx(t, docxPart{name: `word\..\x`, data: "x"})), "bs.docx isn't a Word document Portenv can pass on safely (a part is named outside the document)."},
		{"macros", "m.docx", string(goodDocx(t, docxPart{name: "word/vbaProject.bin", data: "x"})), "m.docx isn't a Word document Portenv can pass on safely (it has macros)."},
		{"not Word", "zip.docx", string(docxOf(t, docxPart{name: "a.txt", data: "x"})), "zip.docx isn't a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."},
		{"too large", "big.png", string(bytes.Repeat([]byte("x"), teamkit.MaxAttachmentBytes+1)), "big.png is 20 MB; an attachment can be at most 20 MB. Make it smaller and attach it again."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, h, store := newAttachEngine(t)
			r, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"lead"}, "", [2]string{c.file, c.data}))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.GetMessages()) != 0 || !slices.Equal(r.GetRefused(), []string{c.want}) {
				t.Errorf("result %v, want %q", r, c.want)
			}
			if len(h.homes["lead"]) != 0 || len(storeEntries(t, store)) != 0 || len(h.boardLines()) != 0 {
				t.Error("something landed")
			}
		})
	}
	e, h, _ := newAttachEngine(t)
	r, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"lead"}, "", [2]string{"Spec v2.docx", string(goodDocx(t))}))
	if err != nil || len(r.GetMessages()) != 1 {
		t.Fatalf("a Word document: %v, %v", r, err)
	}
	if a := r.GetMessages()[0].GetAttachments()[0]; a.GetMediaType() != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || h.homes["lead"][a.GetInboxPath()] == nil {
		t.Errorf("docx %v", a)
	}
	// More than 5 files, an agent not in the team, a pane with two agents.
	six := sendFiles("w", []string{"lead"}, "", [2]string{"1.txt", "1"}, [2]string{"2.txt", "2"}, [2]string{"3.txt", "3"}, [2]string{"4.txt", "4"}, [2]string{"5.txt", "5"}, [2]string{"6.txt", "6"})
	if _, err := e.sendAttachments(context.Background(), six); err == nil || err.Error() != "Attach at most 5 files at once." {
		t.Errorf("six: %v", err)
	}
	if _, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"nobody"}, "", [2]string{"a.txt", "a"})); err == nil {
		t.Error("to an agent not in the team")
	}
	if _, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"lead", "builder"}, "%1", [2]string{"a.txt", "a"})); err == nil {
		t.Error("a pane drop for two agents")
	}
}

// Dropped on an agent's pane: after the copy lands, the helper offers the
// in-box path; how it did comes back to the app.
func TestADropOnAPaneOffersThePath(t *testing.T) {
	e, h, _ := newAttachEngine(t)
	r, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"builder"}, "%3", [2]string{"SPEC.md", "# Spec"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.inserted) != 1 || h.inserted[0].GetAgentId() != "builder" || h.inserted[0].GetPaneId() != "%3" ||
		h.inserted[0].GetBoxPath() != "/home/builder/inbox/owner/20261017T090000Z/SPEC.md" {
		t.Errorf("inserted %v", h.inserted)
	}
	if r.GetInserted() != typesv1.InsertHow_INSERT_HOW_TYPED || r.GetInsertedPath() != "/home/builder/inbox/owner/20261017T090000Z/SPEC.md" {
		t.Errorf("result %v", r)
	}
}

// ADR 0030 point 3's names.
func TestAttachmentNames(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"login-390.png", ".png"}:         "login-390.png",
		{"notes.pdf", ".png"}:             "notes.png",
		{"../../etc/passwd", ".txt"}:      "passwd.txt",
		{"a/b\\c:d.md", ".md"}:            "b-c-d.md",
		{".hidden.md", ".md"}:             "hidden.md",
		{"--force.txt", ".txt"}:           "force.txt",
		{"trailing. . .", ".txt"}:         "trailing.txt",
		{"evil\u202egnp.exe", ".png"}:     "evilgnp.png",
		{"zero\u200bwidth.md", ".md"}:     "zerowidth.md",
		{"bell\a\x1b[2J.txt", ".txt"}:     "bell[2J.txt",
		{"", ".png"}:                      "attachment.png",
		{"....", ".pdf"}:                  "attachment.pdf",
		{"Café.md", ".md"}:               "Café.md",
		{"report", ".pdf"}:                "report.pdf",
		{strings.Repeat("é", 100), ".md"}: strings.Repeat("é", 58) + ".md",
		{"a\x00b.txt", ".txt"}:            "a-b.txt",
	} {
		if got := cleanAttachName(in[0], in[1]); got != want {
			t.Errorf("clean(%q, %s) = %q, want %q", in[0], in[1], got, want)
		}
		if got := cleanAttachName(in[0], in[1]); len(got) > teamkit.MaxAttachmentNameBytes || !boardText(got, false) {
			t.Errorf("clean(%q) = %q isn't a safe name", in[0], got)
		}
	}
}

// The AGENTS.md block says how to attach, and the default (R-0380).
func TestAgentsMDSaysHowToAttach(t *testing.T) {
	team := defaultTeam()
	if s := agentsMDBlock(&Team{Agents: team}, &team[1], "w"); !strings.Contains(s, teamkit.AttachAgentsMDLine) {
		t.Errorf("AGENTS.md block: %s", s)
	}
}

func FuzzSniffAttachment(f *testing.F) {
	for _, s := range []string{pdfBytes, "# md", "\x89PNG\r\n\x1a\n", string(webpOf(3, 3)), "PK\x03\x04", "GIF89a", "\xff\xd8\xff"} {
		f.Add([]byte(s), "a.md", false)
	}
	f.Fuzz(func(t *testing.T, data []byte, name string, inbound bool) {
		typ, line := sniffAttachment(bytes.NewReader(data), int64(len(data)), name, inbound)
		if (line == "") == (typ.mediaType == "") {
			t.Errorf("type %q and line %q", typ.mediaType, line)
		}
		if typ.mediaType != "" && !strings.HasPrefix(typ.ext, ".") {
			t.Errorf("type %q has extension %q", typ.mediaType, typ.ext)
		}
	})
}

func FuzzCleanAttachName(f *testing.F) {
	for _, s := range []string{"a.png", "../x", "\u202e", ".", "-", "a\x00b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := cleanAttachName(name, ".txt")
		if len(got) > teamkit.MaxAttachmentNameBytes || !boardText(got, false) || strings.ContainsAny(got, "/\\:\x00") ||
			strings.HasPrefix(got, ".") || strings.HasPrefix(got, "-") || !strings.HasSuffix(got, ".txt") {
			t.Errorf("clean(%q) = %q", name, got)
		}
	})
}

// The board's first load (and its sweep) comes before the owner's files
// are put in place, so the sweep never takes a file whose line isn't
// written yet.
func TestAFirstLoadNeverSweepsTheOwnersFileInFlight(t *testing.T) {
	e, h, store := newAttachEngine(t)
	e.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) {
		return append(h.boardLines(), &agentv1.ReadBoardResponse{Complete: true}), nil
	}
	r, err := e.sendAttachments(context.Background(), sendFiles("w", []string{"lead"}, "", [2]string{"SPEC.md", "# Spec"}))
	if err != nil || len(r.GetMessages()) != 1 {
		t.Fatalf("%v, %v", r, err)
	}
	id := r.GetMessages()[0].GetAttachments()[0].GetId()
	if !slices.Contains(storeEntries(t, store), id) {
		t.Errorf("the owner's file was swept: %v", storeEntries(t, store))
	}
}
