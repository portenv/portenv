// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Attachments (ADR 0030 points 2 and 8): the helper reads a file an agent
// attaches, as the agent, from its own home only, and offers a file the
// owner dropped on an agent's pane. portenvd makes every other check.

// attachRefusal is why a file can't be attached, as one word portenvd
// turns into its plain line: outside, missing, link, folder, special,
// not-own, too-large. It crosses the as-agent process in its text.
type attachRefusal string

func (r attachRefusal) Error() string { return "attach refused: " + string(r) }

var attachRefusalRE = regexp.MustCompile(`attach refused: ([a-z-]+)`)

// attachOwned is whether the opened file is the caller's own: the read
// runs as the agent, so its uid is the agent's. Tests replace it.
var attachOwned = func(st *unix.Stat_t) bool { return ownedByMe("", st) }

// attachRead is "team-io attach-read HOME REL MAX", run as the agent: it
// opens the home and walks rel one component at a time with
// O_NOFOLLOW through descriptors (the hand-off walker), refuses a link
// anywhere, a folder or a special file, checks the opened file is the
// agent's own and within max, and writes at most max bytes.
func attachRead(home, rel string, max int64, w io.Writer) error {
	parts, err := homeRel(rel, false)
	if err != nil || teamkit.HiddenComponent(rel) {
		return attachRefusal("outside")
	}
	dfd, err := openDirs(home, parts[:len(parts)-1], false)
	if err != nil {
		return walkRefusal(err)
	}
	defer func() { _ = unix.Close(dfd) }()
	name := parts[len(parts)-1]
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return walkRefusal(err)
	}
	if r := modeRefusal(uint32(st.Mode)); r != "" { //nolint:unconvert // uint16 on darwin
		return r
	}
	// O_NONBLOCK: a FIFO swapped in after the stat never blocks the open.
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return walkRefusal(err)
	}
	f := os.NewFile(uintptr(fd), rel) // #nosec G115 -- a descriptor
	defer func() { _ = f.Close() }()
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if r := modeRefusal(uint32(st.Mode)); r != "" { //nolint:unconvert // uint16 on darwin
		return r
	}
	if !attachOwned(&st) {
		return attachRefusal("not-own")
	}
	if st.Size > max {
		return attachRefusal("too-large")
	}
	return copyAtMost(w, f, max)
}

// modeRefusal refuses anything but a regular file.
func modeRefusal(mode uint32) attachRefusal {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return ""
	case unix.S_IFLNK:
		return "link"
	case unix.S_IFDIR:
		return "folder"
	}
	return "special"
}

// walkRefusal names a failed step: a link where a folder or the file
// should be, or nothing there.
func walkRefusal(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EMLINK), errors.Is(err, unix.ENOTDIR):
		// O_DIRECTORY|O_NOFOLLOW on a link is ELOOP or ENOTDIR, by system.
		return attachRefusal("link")
	case errors.Is(err, unix.ENOENT):
		return attachRefusal("missing")
	}
	return err
}

// copyAtMost copies at most max+1 bytes: one more than max refuses the
// file, so one that grows while it's read is refused, never trimmed.
func copyAtMost(w io.Writer, r io.Reader, max int64) error {
	n, err := io.Copy(w, io.LimitReader(r, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return attachRefusal("too-large")
	}
	return nil
}

// attachSource maps an absolute box path to the path in the agent's home:
// /home/<agent>/ only, clean and plain.
func attachSource(agent, p string) (string, bool) {
	rel, ok := strings.CutPrefix(p, "/home/"+agent+"/")
	if !ok || path.Clean(p) != p || !plainText(p, false) {
		return "", false
	}
	if _, err := homeRel(rel, false); err != nil {
		return "", false
	}
	return rel, true
}

// ReadAttachmentSource streams a file an agent attaches (ADR 0030 point
// 2): the size first, the bytes in 256 KB pieces, the SHA-256 last.
func (s *apiServer) ReadAttachmentSource(req *agentv1.ReadAttachmentSourceRequest, stream agentv1.AgentService_ReadAttachmentSourceServer) error {
	return s.readAttachmentSource(stream.Context(), req, stream.Send)
}

func (s *apiServer) readAttachmentSource(ctx context.Context, req *agentv1.ReadAttachmentSourceRequest, send func(*agentv1.ReadAttachmentSourceResponse) error) error {
	max := req.GetMaxBytes()
	if max <= 0 || max > teamkit.MaxAttachmentBytes {
		return status.Errorf(codes.InvalidArgument, "an attachment's limit is 1 byte to %d MB", teamkit.MaxAttachmentBytes>>20)
	}
	uid, home, err := s.agentHome(req.GetAgentId())
	if err != nil {
		return err
	}
	rel, ok := attachSource(req.GetAgentId(), req.GetPath())
	if !ok {
		return status.Error(codes.FailedPrecondition, attachRefusal("outside").Error())
	}
	out, err := s.homeIO(ctx, uid, []string{"attach-read", home, rel, strconv.FormatInt(max, 10)}, nil)
	if err != nil {
		if m := attachRefusalRE.FindStringSubmatch(err.Error()); m != nil {
			return status.Error(codes.FailedPrecondition, attachRefusal(m[1]).Error())
		}
		return status.Errorf(codes.Internal, "read the attachment: %v", err)
	}
	if int64(len(out)) > max {
		return status.Error(codes.FailedPrecondition, attachRefusal("too-large").Error())
	}
	h := sha256.Sum256(out)
	if err := send(&agentv1.ReadAttachmentSourceResponse{Size: int64(len(out))}); err != nil {
		return err
	}
	for len(out) > 0 {
		n := min(len(out), teamkit.AttachmentReadChunkBytes)
		if err := send(&agentv1.ReadAttachmentSourceResponse{Data: out[:n]}); err != nil {
			return err
		}
		out = out[n:]
	}
	return send(&agentv1.ReadAttachmentSourceResponse{Sha256: hex.EncodeToString(h[:])})
}

// dropPaneState is what the helper's per-pane parser knows of a pane
// (ADR 0020, ADR 0017), for a dropped path.
type dropPaneState struct {
	// known: a parser follows the pane; without one nothing is typed.
	known bool
	// last is the pane's last OSC 133 mark on the main screen.
	last paneMarkKind
	// alt: a full-screen program's own screen.
	alt bool
	// bracketed: the program turned bracketed paste on (mode 2004).
	bracketed bool
}

// dropHow is ADR 0030 point 8's rule: pasted into the agent's runner when
// it turned bracketed paste on, typed at a shell prompt (a prompt drawn
// and no command started since), and nothing anywhere else.
func dropHow(st dropPaneState, runner bool) typesv1.InsertHow {
	switch {
	case !st.known:
	case runner && st.bracketed:
		return typesv1.InsertHow_INSERT_HOW_PASTED
	case !st.alt && (st.last == markPrompt || st.last == markInput):
		return typesv1.InsertHow_INSERT_HOW_TYPED
	}
	return typesv1.InsertHow_INSERT_HOW_NONE
}

// quoteDropped is the path single-quoted for the shell, each ' written as
// '\”, and one space after it. A path with a control or bidi character
// (a newline, an escape sequence, a bracketed paste's end) is refused,
// whatever made it.
func quoteDropped(p string) (string, error) {
	if p == "" || !plainText(p, false) || strings.ContainsRune(p, 0x7f) {
		return "", errors.New("a dropped path is one line of plain text")
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "' ", nil
}

// paneDropState reads a pane's state from the agent's control client: a
// pane no client follows is unknown.
func (s *apiServer) paneDropState(a *agentServer, pane string) dropPaneState {
	if s.dropState != nil {
		return s.dropState(a, pane)
	}
	if a.hub == nil {
		return dropPaneState{}
	}
	c := a.hub.client(agentSession)
	if c == nil {
		return dropPaneState{}
	}
	c.pmu.Lock()
	p := c.parsers[pane]
	c.pmu.Unlock()
	if p == nil {
		return dropPaneState{}
	}
	return p.dropState()
}

// dropState is the parser's state for a dropped path.
func (p *paneParser) dropState() dropPaneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := dropPaneState{known: p.emu != nil, alt: p.alt, bracketed: p.bracketedPaste}
	if n := len(p.marks); n > 0 {
		st.last = p.marks[n-1].kind
	}
	return st
}

// InsertDroppedPath offers a file the owner dropped on an agent's pane
// (ADR 0030 point 8): the copy's path, in the agent's ~/inbox/owner/,
// typed at a shell prompt or pasted into its runner, quoted and never
// with Enter. Anywhere else nothing is typed, and the app shows the path.
func (s *apiServer) InsertDroppedPath(ctx context.Context, req *agentv1.InsertDroppedPathRequest) (*agentv1.InsertDroppedPathResponse, error) {
	none := &agentv1.InsertDroppedPathResponse{How: typesv1.InsertHow_INSERT_HOW_NONE}
	id, p := req.GetAgentId(), req.GetBoxPath()
	if !agentIDRE.MatchString(id) {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a valid agent id", id)
	}
	rel, ok := attachSource(id, p)
	if !ok || !strings.HasPrefix(rel, "inbox/owner/") {
		return nil, status.Error(codes.InvalidArgument, "a dropped path is a file in the agent's ~/inbox/owner/")
	}
	quoted, err := quoteDropped(p)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	a, ok := s.agents.get(id)
	if !ok {
		return none, nil
	}
	if _, err := a.ownPane(ctx, req.GetPaneId()); err != nil {
		return none, nil //nolint:nilerr // not its pane: nothing typed
	}
	how := dropHow(s.paneDropState(a, req.GetPaneId()), req.GetPaneId() == a.mainPane)
	arg, err := tmuxArg(quoted)
	if err != nil {
		return none, nil //nolint:nilerr // nothing typed: the app shows the path
	}
	switch how {
	case typesv1.InsertHow_INSERT_HOW_TYPED:
		// -l: the text as keys, literally; no Enter is ever sent.
		_, err = a.tmux(ctx, "send-keys", "-t", req.GetPaneId(), "-l", "--", arg)
	case typesv1.InsertHow_INSERT_HOW_PASTED:
		// -p: inside the program's bracketed paste, as one paste; -d
		// deletes the buffer after.
		const buf = "portenv-drop"
		_, err = a.tmux(ctx, "set-buffer", "-b", buf, "--", arg, ";", "paste-buffer", "-p", "-d", "-b", buf, "-t", req.GetPaneId())
	default:
		return none, nil
	}
	if err != nil {
		return none, nil //nolint:nilerr // nothing typed: the app shows the path
	}
	return &agentv1.InsertDroppedPathResponse{How: how}, nil
}

// attachTeamIO is team-io's attach-read: HOME REL MAX.
func attachTeamIO(args []string, w io.Writer) error {
	if len(args) != 4 {
		return errors.New("attach-read HOME PATH MAX")
	}
	max, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil || max <= 0 || max > teamkit.MaxAttachmentBytes {
		return fmt.Errorf("attach-read: a bad limit %q", args[3])
	}
	return attachRead(args[1], args[2], max, w)
}

var (
	attachIDRE  = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	attachSumRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// attachMediaTypes are the types ADR 0030 point 1 allows on the board.
	attachMediaTypes = map[string]bool{
		"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "application/pdf": true,
		"text/markdown": true, "text/plain": true, "application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	}
)

// checkAttachments checks a board line's attachments again, whatever
// portenvd did: at most 5, each with a ULID, a plain one-line name with no
// slash, a known type, a size, a hex SHA-256, and an inbox path that's
// plain and relative.
func checkAttachments(as []*typesv1.Attachment) error {
	if len(as) > teamkit.MaxAttachmentsPerMessage {
		return status.Errorf(codes.InvalidArgument, "a board line has at most %d attachments", teamkit.MaxAttachmentsPerMessage)
	}
	for _, a := range as {
		n := a.GetName()
		switch {
		case !attachIDRE.MatchString(a.GetId()),
			n == "" || len(n) > 2*teamkit.MaxAttachmentNameBytes || !plainText(n, false) || strings.ContainsAny(n, "/\\:") || strings.HasPrefix(n, "."),
			!attachMediaTypes[a.GetMediaType()],
			a.GetSize() < 0 || a.GetSize() > teamkit.MaxAttachmentBytes,
			!attachSumRE.MatchString(a.GetSha256()),
			a.GetWidth() < 0 || a.GetHeight() < 0:
			return status.Error(codes.InvalidArgument, "a board line's attachments are records with plain names and known types")
		}
		if p := a.GetInboxPath(); p != "" {
			if _, err := homeRel(p, false); err != nil || !plainText(p, false) {
				return status.Error(codes.InvalidArgument, "an attachment's inbox path is a plain path in the recipient's home")
			}
		}
	}
	return nil
}
