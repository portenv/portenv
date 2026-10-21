// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The app's attachment calls (ADR 0030 point 10): the person at this Mac
// only. The server's interceptor refuses an agent caller (ownerOnlyCall);
// each call checks again itself, before anything is looked at, and no
// team call maps to these.

// storeBoxIDRE is a box id that's safe as a folder name.
var storeBoxIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// attachmentsDir is a workspace's folder in this Mac's attachment store:
// the support folder's Attachments/<box-id> (the instance's, so a dev lane
// has its own).
func (s *Server) attachmentsDir(_ context.Context, name string) (string, error) {
	c, err := s.env.LoadBox(name)
	if err != nil {
		return "", err
	}
	if !storeBoxIDRE.MatchString(c.ID) {
		return "", fmt.Errorf("%q isn't a workspace id", c.ID)
	}
	return filepath.Join(s.env.Dir, "Attachments", c.ID), nil
}

func attachStatus(err error) error {
	if errors.Is(err, errNotOnThisMac) {
		return status.Error(codes.NotFound, err.Error())
	}
	return teamStatus(err)
}

// ReadAttachment streams one attachment from this Mac's store to the app.
func (s *Server) ReadAttachment(req *daemonv1.ReadAttachmentRequest, stream daemonv1.DaemonService_ReadAttachmentServer) error {
	if err := ownerOnlyCall(stream.Context(), daemonv1.DaemonService_ReadAttachment_FullMethodName); err != nil {
		return err
	}
	if err := s.teamEng().readAttachment(stream.Context(), req.GetName(), req.GetAttachmentId(), stream.Send); err != nil {
		return attachStatus(err)
	}
	return nil
}

// SendAttachments is the owner's files for agents: a drop, or Attach….
func (s *Server) SendAttachments(stream daemonv1.DaemonService_SendAttachmentsServer) error {
	if err := ownerOnlyCall(stream.Context(), daemonv1.DaemonService_SendAttachments_FullMethodName); err != nil {
		return err
	}
	r, err := s.teamEng().sendAttachments(stream.Context(), stream.Recv)
	if err != nil {
		return attachStatus(err)
	}
	return stream.SendAndClose(r)
}

// RemoveAttachment is Remove from this Mac.
func (s *Server) RemoveAttachment(ctx context.Context, req *daemonv1.RemoveAttachmentRequest) (*daemonv1.RemoveAttachmentResponse, error) {
	if err := ownerOnlyCall(ctx, daemonv1.DaemonService_RemoveAttachment_FullMethodName); err != nil {
		return nil, err
	}
	if err := s.teamEng().removeAttachment(ctx, req.GetName(), req.GetAttachmentId()); err != nil {
		return nil, attachStatus(err)
	}
	return &daemonv1.RemoveAttachmentResponse{}, nil
}
