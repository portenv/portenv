// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// boxIDCheckTimeout bounds the helper's GetVersion before a tab opens.
var boxIDCheckTimeout = 10 * time.Second

// TabTerminal relays one tab's terminal to the workspace helper's
// TabTerminal (ADR 0020), as Terminal relays an attached one: the same
// socket, the same routing to this Mac's box or a server's.
//
// Before any helper is contacted, the caller must be allowed the workspace
// (mayOpen). Before anything is relayed, the helper must answer as the box
// it was reached for (checkBoxID). The helper's open is built here, field
// by field, with the author from the caller: nothing the client sent can
// set it.
func (s *Server) TabTerminal(stream daemonv1.DaemonService_TabTerminalServer) error {
	who, err := callerFrom(stream.Context())
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "the first tab terminal message must be open")
	}
	name := open.GetBox()
	if err := who.mayOpen(name); err != nil {
		return err
	}
	gen := s.boxGen(name)
	ac, boxID, remote, err := s.tabAgent(stream.Context(), name)
	if err != nil {
		return err
	}
	if err := checkBoxID(stream.Context(), ac, boxID); err != nil {
		if remote {
			s.dropRemoteAgent(name)
		}
		return err
	}
	at, err := ac.TabTerminal(stream.Context())
	if err != nil {
		if remote {
			s.dropRemoteAgent(name)
		}
		return agentError(err)
	}
	author, authorAgentID := who.helperAuthor()
	if err := at.Send(&agentv1.TabTerminalRequest{Msg: &agentv1.TabTerminalRequest_Open{Open: &agentv1.TabTerminalOpen{
		Session:       open.GetSession(),
		Tab:           open.GetTab(),
		Size:          agentSize(open.GetSize()),
		Author:        author,
		AuthorAgentId: authorAgentID,
	}}}); err != nil {
		return s.terminalEnd(name, gen, err)
	}
	go func() {
		for {
			r, err := stream.Recv()
			if err != nil {
				_ = at.CloseSend()
				return
			}
			var m agentv1.TabTerminalRequest
			switch v := r.GetMsg().(type) {
			case *daemonv1.TabTerminalRequest_Input:
				m.Msg = &agentv1.TabTerminalRequest_Input{Input: v.Input}
			case *daemonv1.TabTerminalRequest_Resize:
				m.Msg = &agentv1.TabTerminalRequest_Resize{Resize: agentSize(v.Resize)}
			default:
				continue // a second open, or nothing: the stream has its tab
			}
			if err := at.Send(&m); err != nil {
				return
			}
		}
	}()
	for {
		r, err := at.Recv()
		if err != nil {
			return s.terminalEnd(name, gen, err)
		}
		var m daemonv1.TabTerminalResponse
		switch v := r.GetMsg().(type) {
		case *agentv1.TabTerminalResponse_Reset_:
			reset := &daemonv1.TabTerminalReset{Size: daemonSize(v.Reset_.GetSize()), Seed: v.Reset_.GetSeed(), Skipped: v.Reset_.GetSkipped()}
			for _, a := range v.Reset_.GetAuthors() {
				if da := authorFor(a, who); da != nil {
					reset.Authors = append(reset.Authors, da)
				}
			}
			m.Msg = &daemonv1.TabTerminalResponse_Reset_{Reset_: reset}
		case *agentv1.TabTerminalResponse_Output:
			m.Msg = &daemonv1.TabTerminalResponse_Output{Output: v.Output}
		case *agentv1.TabTerminalResponse_Resized:
			m.Msg = &daemonv1.TabTerminalResponse_Resized{Resized: daemonSize(v.Resized)}
		case *agentv1.TabTerminalResponse_End:
			m.Msg = &daemonv1.TabTerminalResponse_End{End: endFor(v.End)}
		case *agentv1.TabTerminalResponse_Author:
			da := authorFor(v.Author, who)
			if da == nil {
				continue // dropped: not passed on, never repaired
			}
			m.Msg = &daemonv1.TabTerminalResponse_Author{Author: da}
		default:
			continue
		}
		if err := stream.Send(&m); err != nil {
			return err
		}
		if _, ended := m.Msg.(*daemonv1.TabTerminalResponse_End); ended {
			return nil
		}
	}
}

// tabAgent is the helper of the box name, on this Mac or on a server
// (agentFor), the box ID it must answer as, and whether it's a server's.
func (s *Server) tabAgent(ctx context.Context, name string) (agentv1.AgentServiceClient, string, bool, error) {
	remote := s.location(name) != ""
	var id string
	if remote {
		c, err := s.env.LoadBox(name)
		if err != nil {
			return nil, "", true, status.Errorf(codes.FailedPrecondition, "%s: %v", name, err)
		}
		id = c.ID
	} else {
		ob, err := s.get(name)
		if err != nil {
			return nil, "", false, err
		}
		id = ob.sess.Cfg.ID
	}
	ac, err := s.agentFor(ctx, name)
	if err != nil {
		return nil, "", remote, err
	}
	return ac, id, remote, nil
}

// checkBoxID refuses a helper that answers as another box than want, or as
// no box (R-0172, R-0173). This catches a misrouted channel (a stale
// forward, a reused port); it isn't proof of identity, which stays with the
// channel's per-box certificate and token.
//
// An empty box_id is refused here, though older helpers are let through
// elsewhere: TabTerminal and box_id came in the same helper (1.2c PR 3a),
// so a helper that can serve a tab terminal always has the field, and
// every driver sets it (docker, and the runner through it).
func checkBoxID(ctx context.Context, ac agentv1.AgentServiceClient, want string) error {
	ctx, cancel := context.WithTimeout(ctx, boxIDCheckTimeout)
	defer cancel()
	v, err := ac.GetVersion(ctx, &agentv1.GetVersionRequest{})
	if err != nil {
		return agentError(err)
	}
	switch got := v.GetBoxId(); {
	case got == "":
		return status.Error(codes.FailedPrecondition, "the workspace helper didn't say which workspace it runs in, so nothing was sent to it; restart the workspace")
	case want == "" || got != want:
		return status.Error(codes.FailedPrecondition, "the connection reached another workspace's helper, so nothing was sent to it; restart the workspace")
	}
	return nil
}

// authorFor is the helper's CommandAuthor as one recipient sees it: is_you
// is portenvd's to set, from that recipient's caller, never the helper's.
// An author whose name portenvd won't pass on (validAuthorName) is nil.
func authorFor(a *agentv1.CommandAuthor, who caller) *daemonv1.CommandAuthor {
	if !validAuthorName(a.GetName()) || !validAuthorName(a.GetAgentId()) {
		return nil
	}
	id := a.GetAgentId()
	isYou := (who.kind == callerApp && id == "") || (who.kind == callerAgent && id != "" && id == who.id)
	return &daemonv1.CommandAuthor{Line: a.GetLine(), Name: a.GetName(), IsYou: isYou, AgentId: id}
}

// endFor maps the helper's end value by value; one this portenvd doesn't
// know is UNSPECIFIED.
func endFor(e agentv1.TabTerminalEnd) typesv1.TabTerminalEnd {
	switch e {
	case agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED:
		return typesv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED
	case agentv1.TabTerminalEnd_TAB_TERMINAL_END_BOX_STOPPING:
		return typesv1.TabTerminalEnd_TAB_TERMINAL_END_BOX_STOPPING
	default:
		return typesv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED
	}
}

func agentSize(z *daemonv1.TerminalSize) *agentv1.TerminalSize {
	return &agentv1.TerminalSize{Cols: z.GetCols(), Rows: z.GetRows()}
}

func daemonSize(z *agentv1.TerminalSize) *daemonv1.TerminalSize {
	return &daemonv1.TerminalSize{Cols: z.GetCols(), Rows: z.GetRows()}
}
