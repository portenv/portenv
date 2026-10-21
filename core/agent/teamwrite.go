// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The New-workspace team's agent files (ADR 0025 §5), as work: into the
// project's .portenv/agents/, or its off/ folder for an agent left out
// (the folder `portenv team init` uses, which Portenv doesn't load). A
// file that's there is left as it is; nothing is followed through a link,
// and nothing in the files is run.

// teamFilePathRE is a file's path under .portenv/agents/.
var teamFilePathRE = regexp.MustCompile(`^(off/)?[a-z][a-z0-9-]{0,31}\.md$`)

// maxTeamFiles caps one write: 8 agents on, and as many off.
const maxTeamFiles = 32

// WriteAgentFilesResult is write-agent-files' answer.
type WriteAgentFilesResult struct {
	Written     []string `json:"written"`
	Existing    []string `json:"existing"`
	MadeProject bool     `json:"made_project"`
}

// writeAgentFiles writes files into project/.portenv/agents/. A missing
// project is made (0755) and git init'ed on main with git, as the caller.
// Every name is checked before anything is made.
func writeAgentFiles(ctx context.Context, project string, files []teamIOFile, git gitFunc) (WriteAgentFilesResult, error) {
	var res WriteAgentFilesResult
	if !filepath.IsAbs(project) || filepath.Clean(project) != project {
		return res, fmt.Errorf("%q isn't a project folder", project)
	}
	if len(files) > maxTeamFiles {
		return res, fmt.Errorf("%d agent files is more than a team has", len(files))
	}
	for _, f := range files {
		if !teamFilePathRE.MatchString(f.Name) {
			return res, fmt.Errorf("%q isn't an agent file's name", f.Name)
		}
		if len(f.Content) > maxAgentFile {
			return res, fmt.Errorf("%s is over 64 KB", f.Name)
		}
	}
	fi, err := os.Lstat(project)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(project, 0o755); err != nil { // #nosec G301 -- the project, read by the team
			return res, err
		}
		res.MadeProject = true
		if out, err := git(ctx, project, "init", "--quiet", "--initial-branch=main"); err != nil {
			return res, cmdError("start git in the new project", out, err)
		}
	case err != nil:
		return res, err
	case !fi.IsDir():
		return res, fmt.Errorf("%s isn't a folder", project)
	}
	agents := filepath.Join(project, ".portenv", "agents")
	for _, d := range []string{filepath.Join(project, ".portenv"), agents} {
		if err := ensureRealDir(d); err != nil {
			return res, err
		}
	}
	for _, f := range files {
		dir, name := agents, f.Name
		if rest, ok := strings.CutPrefix(f.Name, "off/"); ok {
			dir, name = filepath.Join(agents, "off"), rest
			if err := ensureRealDir(dir); err != nil {
				return res, err
			}
		}
		h, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644) // #nosec G302 G304 -- an agent file, read by the team; checked name
		if errors.Is(err, fs.ErrExist) {
			res.Existing = append(res.Existing, f.Name)
			continue
		}
		if err != nil {
			return res, err
		}
		if _, err := h.Write(f.Content); err != nil {
			_ = h.Close()
			return res, err
		}
		if err := h.Close(); err != nil {
			return res, err
		}
		res.Written = append(res.Written, f.Name)
	}
	return res, nil
}

// teamIOWriteAgentFiles is "team-io write-agent-files PROJECT": the files
// as JSON on stdin, the result as JSON on stdout.
func teamIOWriteAgentFiles(project string, stdin io.Reader, stdout io.Writer) error {
	var files []teamIOFile
	if err := json.NewDecoder(io.LimitReader(stdin, maxTeamFiles*(maxAgentFile*2)+4096)).Decode(&files); err != nil {
		return fmt.Errorf("read the agent files: %w", err)
	}
	res, err := writeAgentFiles(context.Background(), project, files, homeGit(filepath.Dir(project)))
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(res)
}

// WriteAgentFiles writes the New-workspace team's files as work.
func (s *apiServer) WriteAgentFiles(ctx context.Context, req *agentv1.WriteAgentFilesRequest) (*agentv1.WriteAgentFilesResponse, error) {
	prefix := "/home/" + s.cfg.User + "/"
	p := req.GetProjectDir()
	name := strings.TrimPrefix(p, prefix)
	if !strings.HasPrefix(p, prefix) || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || filepath.Clean(p) != p {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a project folder in %s", p, prefix)
	}
	files := make([]teamIOFile, 0, len(req.GetFiles()))
	for _, f := range req.GetFiles() {
		files = append(files, teamIOFile{Name: f.GetName(), Content: f.GetContent()})
	}
	in, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	out, err := s.runTeamIO(ctx, []string{"write-agent-files", filepath.Join(s.cfg.HomeRoot, s.cfg.User, name)}, strings.NewReader(string(in)))
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "write the agent files: %v", err)
	}
	var res WriteAgentFilesResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, status.Errorf(codes.Internal, "write the agent files: %v", err)
	}
	return &agentv1.WriteAgentFilesResponse{Written: res.Written, Existing: res.Existing, MadeProject: res.MadeProject}, nil
}
