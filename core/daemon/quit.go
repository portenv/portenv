// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	if s.stopping || s.relaunching.Load() {
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

// Relaunch implements daemonv1.DaemonServiceServer: Portenv is relaunching
// for an update. This daemon stops serving without closing any box, so
// boxes and their programs keep running, and the next portenvd takes them
// over by re-keying (ADR 0014). No new open starts meanwhile.
func (s *Server) Relaunch(context.Context, *daemonv1.RelaunchRequest) (*daemonv1.RelaunchResponse, error) {
	s.relaunching.Store(true)
	s.log.Info("relaunching: leaving boxes running for the next portenvd")
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		// After this reply has gone out.
		time.AfterFunc(100*time.Millisecond, stop)
	}
	return &daemonv1.RelaunchResponse{}, nil
}

// LeaveUnsaved implements daemonv1.DaemonServiceServer: Quit Anyway, with
// portenvd run by launchd so it outlives the app. The quit marker is
// written and portenvd keeps the box, retrying the save in the background
// until one succeeds (saveInBackground). Until then the box stays open
// here, so the lease stays with this Mac: another machine never opens an
// older save while unsaved work sits here.
func (s *Server) LeaveUnsaved(_ context.Context, req *daemonv1.LeaveUnsavedRequest) (*daemonv1.LeaveUnsavedResponse, error) {
	name := req.GetName()
	ob, err := s.get(name)
	if err != nil {
		return &daemonv1.LeaveUnsavedResponse{}, nil // not open: nothing to do
	}
	if err := s.writeQuitMarker(ob.sess.Cfg.ID); err != nil {
		return nil, err
	}
	ob.quitUnsaved.Store(true)
	if ob.background.CompareAndSwap(false, true) {
		s.log.Info("left unsaved (Quit Anyway): saving in the background", "box", name)
		go s.saveInBackground(name, ob) // #nosec G118 -- outlives the request on purpose
	}
	return &daemonv1.LeaveUnsavedResponse{}, nil
}

// quitRetry is when the background save after Quit Anyway tries again:
// 30 s, 1, 2 and 5 minutes, then every 5 minutes, never giving up while the
// box holds unsaved work. (Autosave's retries, 1.4, follow it.)
var quitRetry = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

func quitRetryDelay(attempt int) time.Duration {
	d := quitRetry[min(attempt, len(quitRetry)-1)]
	if os.Getenv("PORTENV_TEST_QUIT_RETRY") == "fast" { // e2e only: 1, 2, 4 s, then every 10 s
		d /= 30
	}
	return d
}

// saveInBackground retries closing the box (save, release, stop) until it
// works, the app takes the box back, or portenvd stops. On success the
// marker is cleared and a record kept for the next launch's notification.
func (s *Server) saveInBackground(name string, ob *openBox) {
	ctx := s.bgCtx
	for attempt := 0; ; attempt++ {
		if !s.sleepFor(ctx, quitRetryDelay(attempt)) || !ob.background.Load() {
			return
		}
		actx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		err := s.closeQuit(actx, name)
		cancel()
		if !ob.background.Load() {
			return // taken back while that attempt ran
		}
		if err != nil {
			s.log.Warn("background save after Quit Anyway failed; retrying", "box", name, "attempt", attempt+1, "err", err)
			continue
		}
		s.clearQuitMarker(ob.sess.Cfg.ID)
		if err := s.recordSavedAfterQuit(name, ob.sess.Cfg.ID, time.Now()); err != nil {
			s.log.Error("record the background save", "box", name, "err", err)
		}
		s.log.Info("saved in the background after Quit Anyway; closed and released", "box", name)
		return
	}
}

// takeBackBox: the app opens a box that's being saved in the background.
// It stops the background save (after any attempt in flight) and reports
// whether the box is still open here to take back.
func (s *Server) takeBackBox(ob *openBox) bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.closed {
		return false
	}
	return ob.background.CompareAndSwap(true, false)
}

func (s *Server) sleepFor(ctx context.Context, d time.Duration) bool {
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *Server) closeQuit(ctx context.Context, name string) error {
	if s.closeForQuit != nil {
		return s.closeForQuit(ctx, name)
	}
	return s.closeBox(ctx, name)
}

// stopBackground ends the background saves (portenvd stopping); boxes
// still unsaved keep their marker.
func (s *Server) stopBackground() { s.bgCancel() }

const savedAfterQuitFile = "saved-after-quit.json"

// recordSavedAfterQuit keeps, on disk, that a background save succeeded,
// until the app takes it (survives a portenvd restart).
func (s *Server) recordSavedAfterQuit(name, boxID string, at time.Time) error {
	b, err := json.Marshal(struct {
		Name    string    `json:"name"`
		SavedAt time.Time `json:"saved_at"`
	}{name, at})
	if err != nil {
		return err
	}
	dir := filepath.Join(s.env.Dir, "state", boxID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, savedAfterQuitFile), b, 0o600)
}

// TakeSavedAfterQuit implements daemonv1.DaemonServiceServer: the boxes
// saved in the background after Quit Anyway, given out once.
func (s *Server) TakeSavedAfterQuit(context.Context, *daemonv1.TakeSavedAfterQuitRequest) (*daemonv1.TakeSavedAfterQuitResponse, error) {
	r := &daemonv1.TakeSavedAfterQuitResponse{}
	files, _ := filepath.Glob(filepath.Join(s.env.Dir, "state", "*", savedAfterQuitFile))
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- fixed name in portenvd's own state
		if err != nil {
			continue
		}
		var rec struct {
			Name    string    `json:"name"`
			SavedAt time.Time `json:"saved_at"`
		}
		if json.Unmarshal(b, &rec) == nil {
			r.Saved = append(r.Saved, &daemonv1.SavedAfterQuit{Name: rec.Name, SavedAt: timestamppb.New(rec.SavedAt)})
		}
		_ = os.Remove(f)
	}
	return r, nil
}
