// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Attachments in portenvd (ADR 0030): an agent's files on its messages and
// questions, read by the helper as the agent, checked here, stored on
// this Mac, copied into a teammate's inbox, and only then named on the
// board; the owner's files for agents; the app's reads and removals; and
// the sweep after a complete board read.

// notConnected is the line when the workspace's helper can't be reached.
const notConnected = "Portenv isn't connected to this workspace right now. Try again in a moment."

// inboxTime is the folder an inbox copy lands in: the UTC time, to the
// second, as a hand-off's (ADR 0022 §4b).
const inboxTime = "20060102T150405Z"

func (e *teamEngine) perDay() int {
	if e.attachPerDay > 0 {
		return min(e.attachPerDay, teamkit.MaxAttachmentsPerDay)
	}
	return teamkit.AttachmentsPerDay
}

func (e *teamEngine) storeLimit() int64 {
	if e.attachStoreLimit > 0 {
		return min(e.attachStoreLimit, teamkit.MaxAttachmentStore)
	}
	return teamkit.DefaultAttachmentStore
}

// attachmentsTodayLocked counts what an agent attached in the last 24
// hours, from the board: its messages, and its decisions as asked.
func (b *teamBox) attachmentsTodayLocked(agent string, now time.Time) int {
	n := 0
	for _, ev := range b.events {
		switch {
		case ev.msg != nil && ev.msg.GetFrom() == agent && now.Sub(ev.msg.GetCreatedAt().AsTime()) < 24*time.Hour:
			n += len(ev.msg.GetAttachments())
		case ev.dec != nil && ev.dec.GetFrom() == agent && ev.dec.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING &&
			now.Sub(ev.dec.GetCreatedAt().AsTime()) < 24*time.Hour:
			n += len(ev.dec.GetAttachments())
		}
	}
	return n
}

// attachedText is an attach-only message's text: "attached SPEC.md", or
// "attached 3 files".
func attachedText(as []*typesv1.Attachment) string {
	if len(as) == 1 {
		return "attached " + as[0].GetName()
	}
	return fmt.Sprintf("attached %d files", len(as))
}

// attachOnly is a message that's only its attachments: it wakes no one
// (ADR 0030 point 9a).
func attachOnly(m *typesv1.BoardMessage) bool {
	return len(m.GetAttachments()) > 0 && m.GetText() == attachedText(m.GetAttachments())
}

// attachBatch is one call's attachments: stored, and copied for a
// teammate, until the board line takes them.
type attachBatch struct {
	staged []*stagedAttachment
	recs   []*typesv1.Attachment
}

func (a *attachBatch) discard() {
	for _, s := range a.staged {
		s.discard()
	}
}

var refusalRE = regexp.MustCompile(`attach refused: ([a-z-]+)(?: ([0-9]+))?`)

// sourceLine turns the helper's refusal into its plain line (ADR 0030
// point 2's table).
func sourceLine(err error, me, shown, base string) string {
	m := refusalRE.FindStringSubmatch(status.Convert(err).Message())
	if m == nil {
		if status.Code(err) == codes.Unavailable {
			return notConnected
		}
		return "Portenv couldn't read " + shown + ": " + status.Convert(err).Message()
	}
	switch m[1] {
	case "outside":
		return outsideLine(me)
	case "missing":
		return "There's no file at " + shown + "."
	case "link":
		return shown + " is a link. Attach the file itself."
	case "folder":
		return shown + " is a folder. Attach files one at a time."
	case "not-own":
		return shown + " belongs to another user. Copy it into your home first."
	case "too-large":
		if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
			return tooLargeLine(base, n)
		}
		return fmt.Sprintf("%s is over %d MB; an attachment can be at most %d MB. Make it smaller and attach it again.", base, teamkit.MaxAttachmentBytes>>20, teamkit.MaxAttachmentBytes>>20)
	}
	return shown + " isn't a regular file. Attach the file itself."
}

func outsideLine(me string) string {
	return "Attach files from your own home (/home/" + me + "), by their full path. Copy a file there first."
}

func tooLargeLine(name string, n int64) string {
	return fmt.Sprintf("%s is %s; an attachment can be at most %d MB. Make it smaller and attach it again.", name, humanSize(n), teamkit.MaxAttachmentBytes>>20)
}

func (e *teamEngine) storeFullLine(used int64) string {
	return fmt.Sprintf("This workspace's attachments use %s of %s. Remove some in the preview's … menu, or raise the limit in Settings.", humanSize(used), humanSize(e.storeLimit()))
}

func storeLine(name string, err error) string {
	return "Portenv couldn't keep " + name + " on this Mac: " + err.Error()
}

// attachLocked checks, reads and stores an agent's --attach files, and for
// a teammate copies them into its inbox (ADR 0030 point 2, steps 1 to 5).
// It returns the batch, or the plain line refusing the call, with nothing
// left in the store.
func (e *teamEngine) attachLocked(ctx context.Context, name string, b *teamBox, me *TeamAgent, paths []string, to, messageID, decisionID string) (*attachBatch, string) {
	now := e.now()
	if len(paths) > teamkit.MaxAttachmentsPerMessage {
		return nil, fmt.Sprintf("Attach at most %d files to one message or question.", teamkit.MaxAttachmentsPerMessage)
	}
	if today := b.attachmentsTodayLocked(me.ID, now); today+len(paths) > e.perDay() {
		return nil, fmt.Sprintf("%s has sent %d attachments in the last 24 hours; its limit is %d. Describe the rest in a message.", me.ID, today, e.perDay())
	}
	// The path rule and the names (checks 1 to 3), before anything is read.
	type source struct{ path, shown, base string }
	var srcs []source
	for _, p := range paths {
		rel, ok := strings.CutPrefix(p, "/home/"+me.ID+"/")
		if !ok || path.Clean(p) != p || rel == "" || !boardText(p, false) {
			return nil, outsideLine(me.ID)
		}
		shown := "~/" + rel
		if teamkit.HiddenComponent(rel) || teamkit.SecretName(path.Base(rel)) {
			return nil, teamkit.AttachSecretLine(shown)
		}
		srcs = append(srcs, source{p, shown, path.Base(rel)})
	}
	if e.attachDir == nil {
		return nil, "Portenv can't keep attachments on this Mac right now."
	}
	dir, err := e.attachDir(ctx, name)
	if err == nil {
		err = ensureStoreDir(dir)
	}
	if err != nil {
		return nil, storeLine("attachments", err)
	}
	h, err := e.helper(ctx, name)
	if err != nil {
		return nil, notConnected
	}
	used := storeUsage(dir)
	batch := &attachBatch{}
	fail := func(line string) (*attachBatch, string) {
		batch.discard()
		return nil, line
	}
	var total int64
	for _, src := range srcs {
		st, err := stage(dir, newULID(now), teamkit.MaxAttachmentBytes)
		if err != nil {
			return fail(storeLine(src.base, err))
		}
		batch.staged = append(batch.staged, st)
		if line := readSource(ctx, h, me.ID, src.path, src.shown, src.base, st); line != "" {
			return fail(line)
		}
		f, err := st.open()
		if err != nil {
			return fail(storeLine(src.base, err))
		}
		typ, line := sniffAttachment(f, st.size, src.base, false)
		if line == "" && strings.HasPrefix(typ.mediaType, "text/") {
			// The content check (ADR 0030 point 2, check 4).
			if b, err := io.ReadAll(io.NewSectionReader(f, 0, st.size)); err != nil || teamkit.SecretContent(b) {
				line = teamkit.AttachSecretLine(src.shown)
			}
		}
		_ = f.Close()
		if line != "" {
			return fail(line)
		}
		total += st.size
		batch.recs = append(batch.recs, &typesv1.Attachment{
			Id: st.id, Name: cleanAttachName(src.base, typ.ext), MediaType: typ.mediaType, Size: st.size, Sha256: st.sum(),
			Width: typ.width, Height: typ.height,
		})
	}
	if used+total > e.storeLimit() {
		return fail(e.storeFullLine(used))
	}
	for i, st := range batch.staged {
		if err := st.commit(metaFor(batch.recs[i], me.ID, now, messageID, decisionID)); err != nil {
			return fail(storeLine(batch.recs[i].GetName(), err))
		}
	}
	if to != boardOwner {
		full := func() string {
			return fmt.Sprintf("%s's files are full (%s of %s), so the attachment wasn't sent. Ask the owner to make room.", to, humanSize(defaultHomeQuota), humanSize(defaultHomeQuota))
		}
		if line := copyToInbox(ctx, h, dir, to, me.ID, now, batch.recs, full); line != "" {
			return fail(line)
		}
	}
	return batch, ""
}

// metaFor is an attachment's meta.json.
func metaFor(a *typesv1.Attachment, from string, now time.Time, messageID, decisionID string) attachMeta {
	m := attachMeta{ID: a.GetId(), Name: a.GetName(), MediaType: a.GetMediaType(), Size: a.GetSize(), SHA256: a.GetSha256(),
		Width: a.GetWidth(), Height: a.GetHeight(), From: from, CreatedAt: now.UTC(), MessageIDs: []string{}, DecisionIDs: []string{}}
	if messageID != "" {
		m.MessageIDs = append(m.MessageIDs, messageID)
	}
	if decisionID != "" {
		m.DecisionIDs = append(m.DecisionIDs, decisionID)
	}
	return m
}

// readSource streams one file from the helper, read as the agent, into
// the staged attachment, checking the size it said first and the SHA-256
// it said last.
func readSource(ctx context.Context, h agentv1.AgentServiceClient, me, p, shown, base string, st *stagedAttachment) string {
	rs, err := h.ReadAttachmentSource(ctx, &agentv1.ReadAttachmentSourceRequest{AgentId: me, Path: p, MaxBytes: teamkit.MaxAttachmentBytes})
	if err != nil {
		return sourceLine(err, me, shown, base)
	}
	var size int64 = -1
	var sum string
	for {
		r, err := rs.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sourceLine(err, me, shown, base)
		}
		if size < 0 {
			size = r.GetSize()
		}
		if _, err := st.Write(r.GetData()); err != nil {
			return storeLine(base, err)
		}
		if r.GetSha256() != "" {
			sum = r.GetSha256()
		}
	}
	if err := st.finish(); err != nil {
		return storeLine(base, err)
	}
	switch {
	case st.over || size > teamkit.MaxAttachmentBytes:
		return tooLargeLine(base, max(st.size, size))
	case size != st.size || sum != st.sum():
		return shown + " changed while it was read. Attach it again."
	}
	return ""
}

// copyToInbox writes each attachment's checked bytes into the agent's
// ~/inbox/<from>/<time>/, as the agent (WriteHomeFiles), new files only
// (" (2)" for a name already there), within its home limit, and sets each
// record's inbox_path. full is the line when the home has no room.
func copyToInbox(ctx context.Context, h agentv1.AgentServiceClient, dir, agent, from string, now time.Time, recs []*typesv1.Attachment, full func() string) string {
	folder := "inbox/" + from + "/" + now.UTC().Format(inboxTime)
	var total int64
	for _, r := range recs {
		total += r.GetSize()
	}
	home, err := h.ListHomeFiles(ctx, &agentv1.ListHomeFilesRequest{AgentId: agent})
	if err != nil {
		return "Portenv couldn't reach " + agent + "'s files: " + status.Convert(err).Message()
	}
	if home.GetHomeBytes()+total > defaultHomeQuota {
		return full()
	}
	taken := map[string]bool{}
	if l, err := h.ListHomeFiles(ctx, &agentv1.ListHomeFilesRequest{AgentId: agent, Dir: folder}); err == nil {
		for _, f := range l.GetFiles() {
			taken[path.Base(f.GetPath())] = true
		}
	}
	ws, err := h.WriteHomeFiles(ctx)
	if err != nil {
		return "Portenv couldn't reach " + agent + "'s files: " + status.Convert(err).Message()
	}
	first := true
	for _, r := range recs {
		n := r.GetName()
		for i := 2; taken[n]; i++ {
			n = numbered(r.GetName(), i)
		}
		taken[n] = true
		r.InboxPath = folder + "/" + n
		data, err := os.ReadFile(filepath.Join(dir, r.GetId(), attachData)) // #nosec G304 -- the store's own path, from a ULID
		if err != nil {
			return "Portenv couldn't read " + r.GetName() + " back: " + err.Error()
		}
		for {
			k := min(len(data), teamkit.AttachmentReadChunkBytes)
			m := &agentv1.WriteHomeFilesRequest{Path: r.GetInboxPath(), Data: data[:k], Last: k == len(data)}
			if first {
				m.AgentId, m.HomeQuotaBytes, first = agent, defaultHomeQuota, false
			}
			if err := ws.Send(m); err != nil {
				return "Portenv couldn't copy " + r.GetName() + " to " + agent + ": " + err.Error()
			}
			if data = data[k:]; len(data) == 0 {
				break
			}
		}
	}
	if _, err := ws.CloseAndRecv(); err != nil {
		if status.Code(err) == codes.ResourceExhausted {
			return full()
		}
		return "Portenv couldn't copy the attachment to " + agent + ": " + status.Convert(err).Message()
	}
	return ""
}

// sweepLocked removes the attachments no line of a board read whole
// names (ADR 0030 point 6): only when the read says it was complete.
func (e *teamEngine) sweepLocked(ctx context.Context, name string, lines []*agentv1.ReadBoardResponse) {
	if e.attachDir == nil || !slices.ContainsFunc(lines, (*agentv1.ReadBoardResponse).GetComplete) {
		return
	}
	dir, err := e.attachDir(ctx, name)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, l := range lines {
		for _, a := range append(l.GetMessage().GetAttachments(), l.GetDecision().GetAttachments()...) {
			keep[a.GetId()] = true
		}
	}
	sweepStore(dir, keep, time.Now())
}

// readAttachment streams one attachment from this Mac's store to the app:
// the record, then the bytes.
func (e *teamEngine) readAttachment(ctx context.Context, name, id string, send func(*daemonv1.ReadAttachmentResponse) error) error {
	if e.attachDir == nil {
		return errNotOnThisMac
	}
	dir, err := e.attachDir(ctx, name)
	if err != nil {
		return err
	}
	rec, err := readRecord(dir, id)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, id, attachData), os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- a ULID in the store
	if err != nil {
		return errNotOnThisMac
	}
	defer func() { _ = f.Close() }()
	a := &typesv1.Attachment{Id: rec.ID, Name: rec.Name, MediaType: rec.MediaType, Size: rec.Size, Sha256: rec.SHA256, Width: rec.Width, Height: rec.Height}
	if err := send(&daemonv1.ReadAttachmentResponse{Attachment: a}); err != nil {
		return err
	}
	buf := make([]byte, teamkit.AttachmentReadChunkBytes)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if err := send(&daemonv1.ReadAttachmentResponse{Data: buf[:n]}); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// removeAttachment is Remove from this Mac: the bytes go; every record of
// it on the board stays.
func (e *teamEngine) removeAttachment(ctx context.Context, name, id string) error {
	if e.attachDir == nil {
		return errNotOnThisMac
	}
	dir, err := e.attachDir(ctx, name)
	if err != nil {
		return err
	}
	if _, err := readRecord(dir, id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, id))
}

// inboundFile is one of the owner's files, as it arrives.
type inboundFile struct {
	given string
	st    *stagedAttachment
	line  string
	rec   *typesv1.Attachment
}

// checkInbound checks one of the owner's files once it's whole: its type
// (Word allowed) and its name.
func checkInbound(f *inboundFile) {
	if f.line != "" || f.st == nil {
		return
	}
	if err := f.st.finish(); err != nil {
		f.line = storeLine(f.given, err)
		return
	}
	r, err := f.st.open()
	if err != nil {
		f.line = storeLine(f.given, err)
		return
	}
	defer func() { _ = r.Close() }()
	typ, line := sniffAttachment(r, f.st.size, f.given, true)
	if line != "" {
		f.line = line
		return
	}
	f.rec = &typesv1.Attachment{Id: f.st.id, Name: cleanAttachName(f.given, typ.ext), MediaType: typ.mediaType, Size: f.st.size,
		Sha256: f.st.sum(), Width: typ.width, Height: typ.height}
}

// receiveInbound reads the app's files into the store, one at a time,
// each checked once it's whole.
func receiveInbound(dir string, now time.Time, head *daemonv1.SendAttachmentsRequest, recv func() (*daemonv1.SendAttachmentsRequest, error)) ([]*inboundFile, error) {
	var files []*inboundFile
	var cur *inboundFile
	discardAll := func() {
		for _, f := range files {
			if f.st != nil {
				f.st.discard()
			}
		}
	}
	take := func(m *daemonv1.SendAttachmentsRequest) error {
		if cur == nil {
			if m.GetFileName() == "" && len(m.GetData()) == 0 && m.GetSize() == 0 && !m.GetLastOfFile() {
				return nil // the header alone
			}
			if len(files) == teamkit.MaxAttachmentsPerMessage {
				return fmt.Errorf("Attach at most %d files at once.", teamkit.MaxAttachmentsPerMessage) //nolint:staticcheck // ST1005: a plain line
			}
			// The base name only: the Mac path never leaves the app.
			cur = &inboundFile{given: path.Base("/" + m.GetFileName())}
			files = append(files, cur)
			switch {
			case strings.Trim(cur.given, "/") == "":
				cur.line = "A file needs a name."
			case m.GetSize() > teamkit.MaxAttachmentBytes:
				cur.line = tooLargeLine(cur.given, m.GetSize())
			default:
				st, err := stage(dir, newULID(now), teamkit.MaxAttachmentBytes)
				if err != nil {
					return err
				}
				cur.st = st
			}
		}
		if cur.st != nil && cur.line == "" {
			if _, err := cur.st.Write(m.GetData()); err != nil {
				return err
			}
			if cur.st.over {
				cur.line = tooLargeLine(cur.given, cur.st.size)
			}
		}
		if m.GetLastOfFile() {
			checkInbound(cur)
			cur = nil
		}
		return nil
	}
	for m := head; ; {
		if err := take(m); err != nil {
			discardAll()
			return nil, err
		}
		var err error
		if m, err = recv(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			discardAll()
			return nil, err
		}
	}
	if cur != nil {
		checkInbound(cur)
	}
	return files, nil
}

// sendAttachments is the owner's files for agents (ADR 0030 point 8): each
// checked and stored once, then for each agent a copy in its
// ~/inbox/owner/<time>/ and one board notice, "attached SPEC.md", which
// wakes no one. One agent's full home refuses only its copy. A drop on an
// agent's pane then offers the path there.
func (e *teamEngine) sendAttachments(ctx context.Context, recv func() (*daemonv1.SendAttachmentsRequest, error)) (*daemonv1.SendAttachmentsResponse, error) {
	head, err := recv()
	if err != nil {
		return nil, err
	}
	name, thread, pane := head.GetName(), head.GetThreadId(), head.GetPaneId()
	var to []string
	for _, a := range head.GetTo() {
		if !slices.Contains(to, a) {
			to = append(to, a)
		}
	}
	team, err := e.loadTeam(ctx, name)
	if err != nil {
		return nil, err
	}
	switch {
	case len(to) == 0:
		return nil, errors.New("choose the agents to send it to")
	case pane != "" && len(to) != 1:
		return nil, errors.New("a file dropped on a pane goes to that pane's agent only")
	case !threadOK(thread):
		return nil, errors.New("a thread id is the id of a board message")
	}
	for _, a := range to {
		if team.agent(a) == nil {
			return nil, fmt.Errorf("this workspace has no agent %q", a)
		}
	}
	if e.attachDir == nil {
		return nil, errors.New("Portenv can't keep attachments on this Mac right now.") //nolint:staticcheck // ST1005: a plain line
	}
	dir, err := e.attachDir(ctx, name)
	if err == nil {
		err = ensureStoreDir(dir)
	}
	if err != nil {
		return nil, err
	}
	now := e.now()
	files, err := receiveInbound(dir, now, head, recv)
	if err != nil {
		return nil, err
	}
	out := &daemonv1.SendAttachmentsResponse{}
	var ok []*inboundFile
	var total int64
	discardOK := func() {
		for _, f := range ok {
			f.st.discard()
		}
	}
	for _, f := range files {
		if f.line != "" {
			out.Refused = append(out.Refused, f.line)
			if f.st != nil {
				f.st.discard()
			}
			continue
		}
		ok = append(ok, f)
		total += f.rec.GetSize()
	}
	if len(ok) == 0 {
		return out, nil
	}
	h, err := e.helper(ctx, name)
	if err != nil {
		discardOK()
		return nil, errors.New(notConnected) //nolint:staticcheck // ST1005: a plain line, shown as it is
	}
	// The board is loaded (and any sweep done) before anything is put in
	// place, so a sweep never sees an attachment its line hasn't named yet.
	b, err := e.lockBox(ctx, name)
	if err != nil {
		discardOK()
		return nil, err
	}
	defer b.mu.Unlock()
	if used := storeUsage(dir) - total; used+total > e.storeLimit() {
		discardOK()
		out.Refused = append(out.Refused, e.storeFullLine(used))
		return out, nil
	}
	for _, f := range ok {
		if err := f.st.commit(metaFor(f.rec, boardOwner, now, "", "")); err != nil {
			discardOK()
			return nil, err
		}
	}
	sent := map[string][]string{} // attachment → the messages naming it
	for _, agent := range to {
		recs := make([]*typesv1.Attachment, len(ok))
		for i, f := range ok {
			recs[i] = &typesv1.Attachment{Id: f.rec.GetId(), Name: f.rec.GetName(), MediaType: f.rec.GetMediaType(), Size: f.rec.GetSize(),
				Sha256: f.rec.GetSha256(), Width: f.rec.GetWidth(), Height: f.rec.GetHeight()}
		}
		shown := agent
		if s := team.agent(agent).Spec; s != nil && s.Name != "" {
			shown = s.Name
		}
		what := recs[0].GetName()
		if len(recs) > 1 {
			what = fmt.Sprintf("%d files", len(recs))
		}
		full := func() string {
			return fmt.Sprintf("%s's files are full (%s of %s), so it didn't get %s. Remove some in Agent files, or raise its limit in Settings.", shown, humanSize(defaultHomeQuota), humanSize(defaultHomeQuota), what)
		}
		if line := copyToInbox(ctx, h, dir, agent, boardOwner, now, recs, full); line != "" {
			out.Refused = append(out.Refused, line)
			continue
		}
		m := &typesv1.BoardMessage{Id: newULID(now), From: boardOwner, To: agent, ThreadId: thread, Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE,
			Text: attachedText(recs), CreatedAt: timestamppb.New(now), Attachments: recs}
		if m.ThreadId == "" {
			m.ThreadId = m.Id
		}
		if err := e.appendLocked(ctx, name, b, &agentv1.AppendBoardRequest{Line: &agentv1.AppendBoardRequest_Message{Message: m}}); err != nil {
			out.Refused = append(out.Refused, err.Error())
			continue
		}
		out.Messages = append(out.Messages, m)
		for _, r := range recs {
			sent[r.GetId()] = append(sent[r.GetId()], m.GetId())
		}
	}
	for _, f := range ok {
		ids := sent[f.rec.GetId()]
		if len(ids) == 0 {
			f.st.discard() // no board line names it
			continue
		}
		meta := metaFor(f.rec, boardOwner, now, "", "")
		meta.MessageIDs = ids
		_ = writeRecord(filepath.Join(dir, f.rec.GetId()), meta)
	}
	if pane != "" && len(out.Messages) == 1 {
		out.Inserted, out.InsertedPath = insertDropped(ctx, h, out.Messages[0], pane)
	}
	return out, nil
}

// insertDropped asks the helper to offer each copy's path in the pane it
// was dropped on (ADR 0030 point 8), and returns how the first was, and
// the path the app shows: the file's, or for several their folder's.
func insertDropped(ctx context.Context, h agentv1.AgentServiceClient, m *typesv1.BoardMessage, pane string) (typesv1.InsertHow, string) {
	base := "/home/" + m.GetTo() + "/"
	as := m.GetAttachments()
	shown := base + path.Dir(as[0].GetInboxPath()) + "/"
	if len(as) == 1 {
		shown = base + as[0].GetInboxPath()
	}
	how := typesv1.InsertHow_INSERT_HOW_NONE
	for i, a := range as {
		r, err := h.InsertDroppedPath(ctx, &agentv1.InsertDroppedPathRequest{AgentId: m.GetTo(), PaneId: pane, BoxPath: base + a.GetInboxPath()})
		if err != nil || r.GetHow() == typesv1.InsertHow_INSERT_HOW_NONE {
			break
		}
		if i == 0 {
			how = r.GetHow()
		}
	}
	return how, shown
}
