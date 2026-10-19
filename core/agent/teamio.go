// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The board's one writer and the agent files' reader (ADR 0022 §3, §6).
// The API process checks every line itself (R-0254 rule 3), then runs
// "portenv-agent team-io" as work for the file work, which never follows a
// symlink.

const (
	boardMessages  = "messages.jsonl"
	boardDecisions = "decisions.jsonl"
	maxBoardText   = 8 << 10
	maxBoardDetail = 32 << 10
	maxBoardSum    = 200
	maxAgentFile   = 64 << 10
	// maxBoardLine leaves room for a decision's JSON around its detail.
	maxBoardLine = 256 << 10
)

var (
	boardPartyRE    = regexp.MustCompile(`^(?:[a-z][a-z0-9-]{0,31})$`)
	boardIDRE       = regexp.MustCompile(`^[0-9A-Za-z]{1,40}$`)
	agentFileNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}\.md$`)
)

// plainText is valid UTF-8 with no control or bidi characters, except
// newlines and tabs when multiline.
func plainText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C {
			return false
		}
	}
	return true
}

// checkBoardLine checks a line again in the helper, whatever portenvd did.
func checkBoardLine(l *agentv1.AppendBoardRequest) (file string, err error) {
	bad := func(f string, a ...any) (string, error) {
		return "", status.Errorf(codes.InvalidArgument, f, a...)
	}
	switch {
	case l.GetMessage() != nil:
		m := l.GetMessage()
		if !boardIDRE.MatchString(m.GetId()) || (m.GetThreadId() != "" && !boardIDRE.MatchString(m.GetThreadId())) {
			return bad("a board message needs a plain id")
		}
		if !boardPartyRE.MatchString(m.GetFrom()) || !boardPartyRE.MatchString(m.GetTo()) {
			return bad("a board message's sender and recipient are agent ids, owner or portenv")
		}
		if _, ok := typesv1.MessageType_name[int32(m.GetType())]; !ok || m.GetType() == typesv1.MessageType_MESSAGE_TYPE_UNSPECIFIED {
			return bad("a board message needs a type")
		}
		if t := m.GetText(); strings.TrimSpace(t) == "" || len(t) > maxBoardText || !plainText(t, true) {
			return bad("a board message is 1 to 8 KB of plain text")
		}
		return boardMessages, nil
	case l.GetDecision() != nil:
		d := l.GetDecision()
		if !boardIDRE.MatchString(d.GetId()) || !boardPartyRE.MatchString(d.GetFrom()) {
			return bad("a decision needs a plain id and sender")
		}
		if _, ok := typesv1.DecisionKind_name[int32(d.GetKind())]; !ok || d.GetKind() == typesv1.DecisionKind_DECISION_KIND_UNSPECIFIED {
			return bad("a decision needs a kind")
		}
		if _, ok := typesv1.DecisionState_name[int32(d.GetState())]; !ok || d.GetState() == typesv1.DecisionState_DECISION_STATE_UNSPECIFIED {
			return bad("a decision needs a state")
		}
		if s := d.GetSummary(); utf8.RuneCountInString(s) > maxBoardSum || !plainText(s, false) {
			return bad("a decision's summary is at most 200 characters of plain text on one line")
		}
		if t := d.GetDetail(); len(t) > maxBoardDetail || !plainText(t, true) {
			return bad("a decision's detail is at most 32 KB of plain text")
		}
		if n := d.GetAnswerNote(); len(n) > maxBoardText || !plainText(n, true) {
			return bad("an answer's note is at most 8 KB of plain text")
		}
		// An ask-owner's choices (CC-0501): none, or 2 to 4 one-line labels
		// of at most 80 characters; the owner's choice counts from 1.
		if c := d.GetChoices(); len(c) == 1 || len(c) > 4 || d.GetChoice() < 0 || int(d.GetChoice()) > len(c) {
			return bad("a decision has 2 to 4 choices, or none, and a choice among them")
		}
		for _, c := range d.GetChoices() {
			if strings.TrimSpace(c) == "" || utf8.RuneCountInString(c) > 80 || !plainText(c, false) {
				return bad("a decision's choice is 1 to 80 characters of plain text on one line")
			}
		}
		for _, m := range d.GetMigrationItems() {
			switch {
			case !boardIDRE.MatchString(m.GetId()), !plainText(m.GetPath(), false), !plainText(m.GetNote(), false), len(m.GetPath())+len(m.GetNote()) > 8<<10,
				m.GetAgent() != "" && !boardPartyRE.MatchString(m.GetAgent()), !slices.Contains([]string{"git", "repo", "worktree", "file", "setting"}, m.GetKind()):
				return bad("a decision's migration items hold only plain paths, notes, agent ids and kinds")
			}
		}
		for _, p := range d.GetPackages() {
			if !packageRE.MatchString(p.GetName()) || !plainText(p.GetVersion(), false) || !plainText(p.GetSource(), false) || len(p.GetVersion())+len(p.GetSource()) > 512 {
				return bad("a decision's package list holds only package names, versions and sources")
			}
		}
		return boardDecisions, nil
	}
	return bad("a board line is a message or a decision")
}

func (s *apiServer) boardDir() string {
	return filepath.Join(s.cfg.HomeRoot, s.cfg.User, ".portenv", "board")
}

// AppendBoard writes one line to the board, after checking it again.
func (s *apiServer) AppendBoard(ctx context.Context, req *agentv1.AppendBoardRequest) (*agentv1.AppendBoardResponse, error) {
	file, err := checkBoardLine(req)
	if err != nil {
		return nil, err
	}
	var msg []byte
	if m := req.GetMessage(); m != nil {
		msg, err = protojson.Marshal(m)
	} else {
		msg, err = protojson.Marshal(req.GetDecision())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	msg = bytes.ReplaceAll(msg, []byte("\n"), nil) // protojson never adds one; a line is one line
	if _, err := s.runTeamIO(ctx, []string{"append-board", s.boardDir(), file}, bytes.NewReader(msg)); err != nil {
		return nil, status.Errorf(codes.Internal, "write the board: %v", err)
	}
	return &agentv1.AppendBoardResponse{}, nil
}

// ReadBoard sends the board's messages, then its decisions, each in the
// order written.
func (s *apiServer) ReadBoard(_ *agentv1.ReadBoardRequest, stream agentv1.AgentService_ReadBoardServer) error {
	out, err := s.runTeamIO(stream.Context(), []string{"read-board", s.boardDir()}, nil)
	if err != nil {
		return status.Errorf(codes.Internal, "read the board: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), maxBoardLine+2)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < 2 {
			continue
		}
		r := &agentv1.ReadBoardResponse{}
		switch line[0] {
		case 'M':
			var m typesv1.BoardMessage
			if protojson.Unmarshal(line[2:], &m) != nil {
				continue // a damaged line is skipped, never fatal
			}
			r.Line = &agentv1.ReadBoardResponse_Message{Message: &m}
		case 'D':
			var d typesv1.Decision
			if protojson.Unmarshal(line[2:], &d) != nil {
				continue
			}
			r.Line = &agentv1.ReadBoardResponse_Decision{Decision: &d}
		default:
			continue
		}
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ReadAgentFiles reads the project's .portenv/agents/*.md as work.
func (s *apiServer) ReadAgentFiles(ctx context.Context, req *agentv1.ReadAgentFilesRequest) (*agentv1.ReadAgentFilesResponse, error) {
	prefix := "/home/" + s.cfg.User + "/"
	p := req.GetProjectDir()
	name := strings.TrimPrefix(p, prefix)
	if !strings.HasPrefix(p, prefix) || name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." || filepath.Clean(p) != p {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a project folder in %s", p, prefix)
	}
	out, err := s.runTeamIO(ctx, []string{"read-agent-files", filepath.Join(s.cfg.HomeRoot, s.cfg.User, name)}, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read the agent files: %v", err)
	}
	var files []teamIOFile
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, status.Errorf(codes.Internal, "read the agent files: %v", err)
	}
	r := &agentv1.ReadAgentFilesResponse{}
	for _, f := range files {
		r.Files = append(r.Files, &agentv1.AgentFileContent{Name: f.Name, Content: f.Content})
	}
	return r, nil
}

type teamIOFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}

// TeamIO is "portenv-agent team-io": the file work for the board and the
// agent files, run as work. It never follows a symlink.
//
//	team-io append-board DIR FILE   one line from stdin, appended
//	team-io read-board DIR          "M <line>" then "D <line>" lines
//	team-io read-agent-files DIR    JSON: [{name, content}]
func TeamIO(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: team-io append-board DIR FILE | read-board DIR | read-agent-files DIR")
	}
	switch op, dir := args[0], args[1]; op {
	case "append-board":
		if len(args) != 3 || (args[2] != boardMessages && args[2] != boardDecisions) {
			return errors.New("append-board DIR messages.jsonl|decisions.jsonl")
		}
		line, err := io.ReadAll(io.LimitReader(stdin, maxBoardLine+1))
		if err != nil {
			return err
		}
		if len(line) == 0 || len(line) > maxBoardLine || bytes.ContainsAny(line, "\n\r") {
			return errors.New("a board line is one line")
		}
		if err := ensureRealDir(filepath.Dir(dir)); err != nil {
			return err
		}
		if err := ensureRealDir(dir); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, args[2]), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o644) // #nosec G302 G304 -- the board, work's
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	case "read-board":
		for _, part := range []struct{ file, tag string }{{boardMessages, "M "}, {boardDecisions, "D "}} {
			b, err := readNoFollow(dir, part.file, maxBoardFile)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, line := range bytes.Split(b, []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				if _, err := fmt.Fprintf(stdout, "%s%s\n", part.tag, line); err != nil {
					return err
				}
			}
		}
		return nil
	case "read-agent-files":
		files := []teamIOFile{}
		agents := filepath.Join(dir, ".portenv", "agents")
		for _, d := range []string{dir, filepath.Join(dir, ".portenv"), agents} {
			fi, err := os.Lstat(d)
			if errors.Is(err, fs.ErrNotExist) {
				return json.NewEncoder(stdout).Encode(files)
			}
			if err != nil {
				return err
			}
			if !fi.IsDir() {
				return fmt.Errorf("%s isn't a folder", d)
			}
		}
		entries, err := os.ReadDir(agents)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !agentFileNameRE.MatchString(e.Name()) || !e.Type().IsRegular() {
				continue
			}
			b, err := readNoFollow(agents, e.Name(), maxAgentFile)
			if err != nil {
				continue // too large or gone: the checks say so by its absence
			}
			files = append(files, teamIOFile{Name: e.Name(), Content: b})
		}
		return json.NewEncoder(stdout).Encode(files)
	case "open-project":
		// As work, from init at team start (ADR 0022 §2a): one JSON line,
		// what it found (and so didn't change).
		if len(args) > 3 || (len(args) == 3 && args[2] != "look") {
			return errors.New("open-project PROJECT [look]")
		}
		found, err := openProject(context.Background(), dir, ExecRunner{}, fileProjectOps(), len(args) == 3)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(found)
	case "clone-own":
		// As the agent, through as-agent (ADR 0022 §2a point 2).
		if len(args) != 6 {
			return errors.New("clone-own PROJECT HOME ID QUOTA NAME")
		}
		return cloneOwn(context.Background(), dir, args[2], args[3], args[4], args[5], homeGit(args[2]), stdout)
	case "bundle-own":
		// As the agent, through as-agent (ADR 0022 §2a point 3): its
		// branch's bundle on stdout.
		if len(args) != 5 {
			return errors.New("bundle-own HOME NAME ID thin|full")
		}
		return bundleOwn(context.Background(), dir, args[2], args[3], args[4], streamGit(dir), stdout)
	case "migrate-copy":
		// As the agent, from init's move (ADR 0022 §2a point 6).
		if len(args) != 5 {
			return errors.New("migrate-copy HOME SRC DEST QUOTA")
		}
		return migrateCopy(dir, args[2], args[3], args[4], ownedByMe, stdin, stdout)
	case "lens-grant":
		// As the writer, through as-agent (ADR 0022 §2a point 4).
		if len(args) != 4 {
			return errors.New("lens-grant HOME NAME UIDS")
		}
		return lensGrant(dir, args[2], args[3], fileLensOps())
	case "fetch-agent":
		// As work (ADR 0022 §2a point 3): the bundle on stdin, fetched
		// into the project from that file only.
		if len(args) != 4 {
			return errors.New("fetch-agent PROJECT ID MAXBYTES")
		}
		return fetchAgent(context.Background(), dir, args[2], args[3], homeGit(filepath.Dir(dir)), stdin, stdout)
	case "handoff-pack":
		if len(args) != 3 {
			return errors.New("handoff-pack BASE PATH")
		}
		return handoffPack(dir, args[2], stdout)
	case "handoff-unpack":
		if len(args) != 4 {
			return errors.New("handoff-unpack HOME DEST QUOTA")
		}
		return handoffUnpack(dir, args[2], args[3], stdin, stdout)
	case "list-home":
		if len(args) != 3 {
			return errors.New("list-home HOME DIR")
		}
		return homeList(dir, args[2], stdout)
	case "read-home":
		if len(args) != 3 {
			return errors.New("read-home HOME PATH")
		}
		return homeRead(dir, args[2], stdout)
	case "seed-home":
		// As the agent, at its start (ADR 0022 §2).
		if len(args) < 4 {
			return errors.New("seed-home PROJECT HOME QUOTA PATH…")
		}
		return seedHome(dir, args[2], args[3], args[4:], stdout)
	case "write-home":
		if len(args) != 4 {
			return errors.New("write-home HOME PATH QUOTA")
		}
		return homeWrite(dir, args[2], args[3], stdin, stdout)
	}
	return fmt.Errorf("team-io: unknown operation %q", args[0])
}

// maxBoardFile is a board file's rotation size (ADR 0022 §7).
const maxBoardFile = 10 << 20

// ensureRealDir makes dir if it's missing; an existing one must be a
// folder, not a symlink.
func ensureRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.Mkdir(dir, 0o755) // #nosec G301 -- the board: agents may not write it, the owner reads it
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder", dir)
	}
	return nil
}

// readNoFollow reads dir/name, refusing a symlink (in dir or as the file)
// and anything over limit.
func readNoFollow(dir, name string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s isn't a folder", dir)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- fixed names in checked folders
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a file", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is over %d bytes", name, limit)
	}
	return b, nil
}

// teamIOAs runs team-io as work, from the plain binary, the way path-info
// runs: outside a box (or not as the API process) it runs here.
func (s *apiServer) teamIOAs(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	return runAsMainUser(ctx, s.cfg, append([]string{"team-io"}, args...), stdin, func(out io.Writer) error { return TeamIO(args, stdin, out) })
}
