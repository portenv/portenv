// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// maxHomeUpload caps one drag and drop through the helper.
const maxHomeUpload = 1 << 30

// agentHome is an agent's uid and home, from init's users.json.
func (s *apiServer) agentHome(id string) (int, string, error) {
	if !agentIDRE.MatchString(id) {
		return 0, "", status.Errorf(codes.InvalidArgument, "%q isn't a valid agent id", id)
	}
	t := &TeamUsers{Cfg: s.cfg}
	f, err := t.load()
	if err != nil {
		return 0, "", status.Errorf(codes.Internal, "%v", err)
	}
	rec := f.find(id)
	if rec == nil {
		return 0, "", status.Errorf(codes.NotFound, "no agent %q in this workspace", id)
	}
	return rec.UID, t.home(id), nil
}

func (s *apiServer) homeIO(ctx context.Context, uid int, args []string, stdin io.Reader) ([]byte, error) {
	if s.runHomeIO != nil {
		return s.runHomeIO(ctx, uid, args, stdin)
	}
	return runAsAgent(ctx, uid, append([]string{"team-io"}, args...), stdin, func(w io.Writer) error { return TeamIO(args, stdin, w) })
}

func homeErr(err error) error {
	if errors.Is(err, errHomeFull) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

func toAgentFiles(b []byte) ([]*typesv1.AgentFile, error) {
	var es []homeEntry
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, err
	}
	out := make([]*typesv1.AgentFile, 0, len(es))
	for _, e := range es {
		out = append(out, &typesv1.AgentFile{Path: e.Path, Dir: e.Dir, Size: e.Size, Modified: timestamppb.New(time.Unix(e.Modified, 0))})
	}
	return out, nil
}

// ListHomeFiles lists a folder in an agent's home, as the agent.
func (s *apiServer) ListHomeFiles(ctx context.Context, req *agentv1.ListHomeFilesRequest) (*agentv1.ListHomeFilesResponse, error) {
	if _, err := homeRel(req.GetDir(), true); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	uid, home, err := s.agentHome(req.GetAgentId())
	if err != nil {
		return nil, err
	}
	out, err := s.homeIO(ctx, uid, []string{"list-home", home, req.GetDir()}, nil)
	if err != nil {
		return nil, homeErr(err)
	}
	files, err := toAgentFiles(out)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	size, _ := homeSize(home)
	return &agentv1.ListHomeFilesResponse{Files: files, HomeBytes: size}, nil
}

// ReadHomeFile streams a file from an agent's home, as the agent.
func (s *apiServer) ReadHomeFile(req *agentv1.ReadHomeFileRequest, stream agentv1.AgentService_ReadHomeFileServer) error {
	if _, err := homeRel(req.GetPath(), false); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	uid, home, err := s.agentHome(req.GetAgentId())
	if err != nil {
		return err
	}
	out, err := s.homeIO(stream.Context(), uid, []string{"read-home", home, req.GetPath()}, nil)
	if err != nil {
		return homeErr(err)
	}
	for len(out) > 0 {
		n := min(len(out), 256<<10)
		if err := stream.Send(&agentv1.ReadHomeFileResponse{Data: out[:n]}); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

// WriteHomeFiles adds files to an agent's home (the owner's drag and
// drop), each a new file, as the agent, within its quota.
func (s *apiServer) WriteHomeFiles(stream agentv1.AgentService_WriteHomeFilesServer) error {
	var (
		agent string
		quota int64
		uid   int
		home  string
		path  string
		buf   bytes.Buffer
		added []*typesv1.AgentFile
	)
	flush := func() error {
		if path == "" {
			return nil
		}
		out, err := s.homeIO(stream.Context(), uid, []string{"write-home", home, path, strconv.FormatInt(quota, 10)}, bytes.NewReader(buf.Bytes()))
		if err != nil {
			return homeErr(err)
		}
		files, err := toAgentFiles(out)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		added = append(added, files...)
		path = ""
		buf.Reset()
		return nil
	}
	for first := true; ; first = false {
		m, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := flush(); err != nil {
				return err
			}
			return stream.SendAndClose(&agentv1.WriteHomeFilesResponse{Added: added})
		}
		if err != nil {
			return err
		}
		if first {
			agent, quota = m.GetAgentId(), m.GetHomeQuotaBytes()
			if quota <= 0 {
				return status.Error(codes.InvalidArgument, "no quota given")
			}
			if uid, home, err = s.agentHome(agent); err != nil {
				return err
			}
		}
		if m.GetPath() != "" && m.GetPath() != path {
			if err := flush(); err != nil {
				return err
			}
			if _, err := homeRel(m.GetPath(), false); err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			path = m.GetPath()
		}
		if path == "" {
			return status.Error(codes.InvalidArgument, "a file's first piece names its path")
		}
		if buf.Len()+len(m.GetData()) > maxHomeUpload {
			return status.Error(codes.ResourceExhausted, fmt.Sprintf("%s is over %d MB", path, maxHomeUpload>>20))
		}
		buf.Write(m.GetData())
		if m.GetLast() {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
