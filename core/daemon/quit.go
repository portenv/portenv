// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// quitMarker records, per box, that Portenv quit before the box could be
// saved (Quit Anyway, or the app ended while a save could not complete). The
// next open says so and saves first thing; a successful save removes it.
const quitMarker = "quit-unsaved"

// openingLimit bounds how long shutdown waits for boxes still opening.
const openingLimit = 5 * time.Minute

func (s *Server) markerPath(boxID string) string {
	return filepath.Join(s.env.Dir, "state", boxID, quitMarker)
}

func (s *Server) writeQuitMarker(boxID string) error {
	p := s.markerPath(boxID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(s.now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func (s *Server) hasQuitMarker(boxID string) bool {
	_, err := os.Stat(s.markerPath(boxID))
	return err == nil
}

func (s *Server) clearQuitMarker(boxID string) {
	if err := os.Remove(s.markerPath(boxID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Error("remove the quit marker", "box", boxID, "err", err)
	}
}

// startOpening registers an open in progress, or refuses it once shutdown
// has begun. The caller calls done when the open ends.
func (s *Server) startOpening() (done func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, status.Error(codes.Unavailable, "portenvd is stopping; open the box again when Portenv starts")
	}
	s.opening.Add(1)
	return s.opening.Done, nil
}

// closeAll saves, releases and stops every open box when the daemon stops.
// Boxes still opening are waited for first (up to openingLimit), and no new
// open starts. A box that can't be saved is left as it is, with the quit
// marker, so the next open says so and saves first thing.
func (s *Server) closeAll() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	waited := make(chan struct{})
	go func() { s.opening.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(openingLimit):
		s.log.Error("a box was still opening after the shutdown wait; closing the others")
	}
	s.mu.Lock()
	open := make(map[string]*openBox, len(s.open))
	for n, ob := range s.open {
		open[n] = ob
	}
	s.mu.Unlock()
	for name, ob := range open {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := s.closeBox(ctx, name)
		cancel()
		if err == nil {
			continue
		}
		s.log.Error("close on shutdown: left as it is, to save on the next open", "box", name, "err", err)
		if ob.sess != nil {
			if merr := s.writeQuitMarker(ob.sess.Cfg.ID); merr != nil {
				s.log.Error("record the quit marker", "box", name, "err", merr)
			}
		}
	}
}

// saveAfterQuit saves a box opened after Portenv quit before saving it, then
// removes the marker. A failed save keeps the marker and says so.
func (s *Server) saveAfterQuit(name string, ob *openBox) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.closed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ob.saving.Store(true)
	_, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SaveAutosave})
	ob.saving.Store(false)
	ob.failed.Store(err != nil)
	if err != nil {
		s.log.Error("save after quitting before saving", "box", name, "err", ob.noteErr(err))
		return
	}
	s.clearQuitMarker(ob.sess.Cfg.ID)
	ob.quitUnsaved.Store(false)
	s.log.Info("saved after quitting before saving", "box", name)
}

// Woke checks every open box's agent channel after the system wakes, and
// restarts a box whose channel is gone (its home stays: rule 3).
func (s *Server) Woke(ctx context.Context, _ *daemonv1.WokeRequest) (*daemonv1.WokeResponse, error) {
	s.mu.Lock()
	names := make([]string, 0, len(s.open))
	for n := range s.open {
		names = append(names, n)
	}
	s.mu.Unlock()
	var out daemonv1.WokeResponse
	for _, name := range names {
		r := &daemonv1.WokeBox{Name: name}
		check, err := s.CheckBox(ctx, &daemonv1.CheckBoxRequest{Name: name})
		if err == nil && check.GetAgentAvailable() {
			r.AgentAvailable = true
		} else {
			s.log.Info("after wake: the box agent's channel is gone; restarting the box", "box", name)
			if _, err := s.RestartBox(ctx, &daemonv1.RestartBoxRequest{Name: name}); err != nil {
				r.Detail = status.Convert(err).Message()
			} else {
				r.Restarted, r.AgentAvailable = true, true
			}
		}
		out.Boxes = append(out.Boxes, r)
	}
	return &out, nil
}
