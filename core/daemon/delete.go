// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Delete Workspace (ADR 0029). portenvd deletes a box in a fixed order,
// each step safe to repeat, recording in the box's config the step it is
// at, so a failure says where it stopped and a call again continues from
// there; nothing is left half-gone silently:
//
//  1. checks: the person at this Mac, the exact name, no agent running,
//     nothing open or saving here, not moving or on a server, saves
//     elsewhere confirmed, nothing running, no other machine holding the
//     lease;
//  2. the instance; 3. the home storage; 4. the saves on this Mac (saves
//     elsewhere stay, and so does the key: no box ever has saves and no
//     key);
//  5. the key: the app deletes it from the Keychain (only the app touches
//     the Keychain) and calls again with keys_deleted;
//  6. the record, last.

// boxDeleter is what deleting a box needs of this Mac (tests set a fake).
type boxDeleter interface {
	// Running: the box's instance runs on this Mac.
	Running(ctx context.Context, c local.BoxConfig) (bool, error)
	// LeaseHolder is the machine whose lease the box's current save
	// carries, "" for none. Reads the repository with the box's key.
	LeaseHolder(ctx context.Context, c local.BoxConfig) (string, error)
	// RemoveInstance stops and removes the box's instance.
	RemoveInstance(ctx context.Context, c local.BoxConfig) error
	// RemoveHome deletes its home storage.
	RemoveHome(ctx context.Context, c local.BoxConfig) error
	// RemoveSaves deletes its repository on this Mac.
	RemoveSaves(ctx context.Context, c local.BoxConfig) error
	// RemoveRecord deletes this Mac's state for it, then its config.
	RemoveRecord(ctx context.Context, c local.BoxConfig) error
}

func (s *Server) boxDeleter() boxDeleter {
	if s.deleter != nil {
		return s.deleter
	}
	return engineDeleter{env: s.env}
}

var deleteStepEnum = map[string]daemonv1.DeleteStep{
	local.DeleteStepInstance: daemonv1.DeleteStep_DELETE_STEP_INSTANCE,
	local.DeleteStepHome:     daemonv1.DeleteStep_DELETE_STEP_HOME,
	local.DeleteStepSaves:    daemonv1.DeleteStep_DELETE_STEP_SAVES,
	local.DeleteStepKey:      daemonv1.DeleteStep_DELETE_STEP_KEY,
	local.DeleteStepRecord:   daemonv1.DeleteStep_DELETE_STEP_RECORD,
}

// deleteStepWords says a step as people read it, after "stopped while".
var deleteStepWords = map[string]string{
	local.DeleteStepInstance: "stopping it",
	local.DeleteStepHome:     "removing its files on this Mac",
	local.DeleteStepSaves:    "deleting its saves",
	local.DeleteStepKey:      "removing its key",
	local.DeleteStepRecord:   "removing it from your workspaces",
}

// onlyThePerson refuses anyone but the person at this Mac: agents never
// delete a workspace (R-0358).
func onlyThePerson(ctx context.Context) error {
	who, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	if who.kind != callerApp {
		return status.Error(codes.PermissionDenied, "Only the person at this Mac can delete a workspace.")
	}
	return nil
}

// loadForDelete is the box's config, or NotFound.
func (s *Server) loadForDelete(name string) (local.BoxConfig, error) {
	c, err := s.env.LoadBox(name)
	if errors.Is(err, fs.ErrNotExist) {
		return c, status.Errorf(codes.NotFound, "There's no workspace named %s here.", name)
	}
	if err != nil {
		var u local.UsageError
		if errors.As(err, &u) {
			return c, status.Error(codes.InvalidArgument, u.Msg)
		}
		return c, err
	}
	return c, nil
}

// deleteRefusal is why the box can't be deleted now, as far as portenvd
// knows without the engine or the key, or "". open reports a box open here.
func (s *Server) deleteRefusal(name string) (refusal string, open bool) {
	if agents := s.runningAgents(name); len(agents) > 0 {
		return fmt.Sprintf("%s has agents running. Stop its agents first (stop its team), then delete it.", name), false
	}
	s.mu.Lock()
	ob, isOpen := s.open[name]
	target, moving := s.moves[name]
	gitBusy := s.gitBusy[name] > 0
	s.mu.Unlock()
	if gitBusy {
		return fmt.Sprintf("%s is pushing to or fetching from GitHub right now. Delete it once that has finished.", name), false
	}
	if moving {
		return fmt.Sprintf("%s is moving to %s. Delete it once the move has finished.", name, serverHost(target)), false
	}
	if isOpen {
		switch {
		case ob.background.Load():
			return fmt.Sprintf("Portenv is still saving %s in the background. Delete it once that save has finished.", name), true
		case ob.saving.Load():
			return fmt.Sprintf("%s is being saved right now. Delete it once the save has finished.", name), true
		}
		return fmt.Sprintf("%s is open. Close it first, then delete it.", name), true
	}
	if host := s.location(name); host != "" {
		return fmt.Sprintf("%s is open on %s. Move it to this Mac first, then delete it.", name, local.ServerDisplayName(host)), false
	}
	return "", false
}

// runningAgents are the agents of the box's team this portenvd knows are
// running (any state but stopped or invalid), without loading anything.
func (s *Server) runningAgents(name string) []string {
	e := s.teamEng()
	e.mu.Lock()
	b := e.boxes[name]
	e.mu.Unlock()
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for id, st := range b.states {
		switch st {
		case typesv1.AgentState_AGENT_STATE_STARTING, typesv1.AgentState_AGENT_STATE_RUNNING, typesv1.AgentState_AGENT_STATE_IDLE,
			typesv1.AgentState_AGENT_STATE_NEEDS_YOU, typesv1.AgentState_AGENT_STATE_CONNECTED:
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// GetDeletePlan implements daemonv1.DaemonServiceServer.
func (s *Server) GetDeletePlan(ctx context.Context, req *daemonv1.GetDeletePlanRequest) (*daemonv1.GetDeletePlanResponse, error) {
	if err := onlyThePerson(ctx); err != nil {
		return nil, err
	}
	c, err := s.loadForDelete(req.GetName())
	if err != nil {
		return nil, err
	}
	p := &daemonv1.GetDeletePlanResponse{BoxId: c.ID, SavesPlace: local.SavesPlace(c), CarriesRecoveryKey: c.Recovery}
	if repo, here := s.env.LocalRepository(c); here {
		n, size, err := local.CountSaves(repo)
		if err != nil {
			s.log.Info("delete plan: counting saves", "box_id", c.ID, "err", noPaths(err.Error()))
		}
		p.SaveCount, p.SavesBytes = int32(min(n, 1<<30)), size // #nosec G115 -- bounded
	} else {
		p.SavesElsewhere = true
	}
	if c.Deleting != nil {
		p.StoppedAt = deleteStepEnum[c.Deleting.Step]
	}
	p.Refusal, p.OpenHere = s.deleteRefusal(c.Name)
	return p, nil
}

// DeleteBox implements daemonv1.DaemonServiceServer.
func (s *Server) DeleteBox(ctx context.Context, req *daemonv1.DeleteBoxRequest) (*daemonv1.DeleteBoxResponse, error) {
	if err := onlyThePerson(ctx); err != nil {
		return nil, err
	}
	name := req.GetName()
	if req.GetConfirmName() != name || name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "To delete %s, type its name exactly as it's shown.", name)
	}
	c, err := s.loadForDelete(name)
	if err != nil {
		return nil, err
	}
	done, err := s.startDeleting(name)
	if err != nil {
		return nil, err
	}
	defer done()
	s.log.Info("delete", "box_id", c.ID)
	if refusal, _ := s.deleteRefusal(name); refusal != "" {
		s.log.Info("delete refused", "box_id", c.ID, "reason", "open, moving, elsewhere or agents running")
		return nil, status.Error(codes.FailedPrecondition, refusal)
	}
	d := s.boxDeleter()
	if c.Deleting == nil {
		if err := s.deleteChecks(ctx, d, c, req.GetConfirmSavesElsewhere()); err != nil {
			return nil, err
		}
		c.Deleting = &local.DeleteProgress{Step: local.DeleteStepInstance}
		if _, here := s.env.LocalRepository(c); !here {
			c.Deleting.SavesLeftAt = local.SavesPlace(c)
		}
		if err := s.env.SaveBox(c); err != nil {
			return nil, err
		}
	}
	return s.runDeleteSteps(ctx, d, c, req.GetKeysDeleted())
}

// startDeleting marks the box as being deleted by this call: one deletion
// at a time, and no open meanwhile.
func (s *Server) startDeleting(name string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleting == nil {
		s.deleting = map[string]bool{}
	}
	if s.deleting[name] {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is already being deleted.", name)
	}
	s.deleting[name] = true
	return func() {
		s.mu.Lock()
		delete(s.deleting, name)
		s.mu.Unlock()
	}, nil
}

// beingDeleted: the box is being deleted, or its deletion stopped part-way:
// it never opens.
func (s *Server) beingDeleted(name string) bool {
	s.mu.Lock()
	now := s.deleting[name]
	s.mu.Unlock()
	if now {
		return true
	}
	c, err := s.env.LoadBox(name)
	return err == nil && c.Deleting != nil
}

// deleteChecks are the checks before the first step, which need the engine
// and the key.
func (s *Server) deleteChecks(ctx context.Context, d boxDeleter, c local.BoxConfig, savesElsewhereConfirmed bool) error {
	if _, here := s.env.LocalRepository(c); !here && !savesElsewhereConfirmed {
		return status.Errorf(codes.FailedPrecondition,
			"The saves of %s are on %s. They stay there, and so does this Mac's key for them, so it can be restored later. Confirm that to delete it from this Mac.",
			c.Name, local.SavesPlace(c))
	}
	running, err := d.Running(ctx, c)
	if err != nil {
		s.log.Info("delete: running check", "box_id", c.ID, "err", noPaths(err.Error()))
		return status.Errorf(codes.Unavailable, "%s Nothing was deleted.", plainEngineLine(err, "Portenv couldn't check whether "+c.Name+" is running."))
	}
	if running {
		return status.Errorf(codes.FailedPrecondition, "%s is still running on this Mac. Open it and close it, then delete it.", c.Name)
	}
	holder, err := d.LeaseHolder(ctx, c)
	if errors.Is(err, keys.ErrNeedsApproval) {
		return keyError(err)
	}
	if err != nil {
		s.log.Info("delete: lease check", "box_id", c.ID, "err", noPaths(err.Error()))
		return status.Errorf(codes.Unavailable, "Portenv couldn't check whether %s is open on another machine, so nothing was deleted. Try again when %s can be reached.", c.Name, local.SavesPlace(c))
	}
	if holder != "" && holder != s.env.Machine {
		return status.Errorf(codes.FailedPrecondition, "%s is open on %s. Close it there first, then delete it. Nothing was deleted.", c.Name, holder)
	}
	return nil
}

// plainEngineLine is the engine's plain line for err, or fallback.
func plainEngineLine(err error, fallback string) string {
	if st, ok := status.FromError(plainOpenError(err)); ok && st.Code() == codes.Unavailable {
		return st.Message()
	}
	return fallback
}

// runDeleteSteps runs the steps from the one recorded, recording each next
// one before going on.
func (s *Server) runDeleteSteps(ctx context.Context, d boxDeleter, c local.BoxConfig, keysDeleted bool) (*daemonv1.DeleteBoxResponse, error) {
	elsewhere := c.Deleting.SavesLeftAt != ""
	// keys_deleted counts only when the saves were already gone as this
	// call began: never before.
	keysGone := keysDeleted && c.Deleting.Step == local.DeleteStepKey
	advance := func(next string) error {
		c.Deleting.Step = next
		return s.env.SaveBox(c)
	}
	stopped := func(err error) error {
		s.log.Warn("delete stopped", "box_id", c.ID, "step", c.Deleting.Step, "err", noPaths(err.Error()))
		return status.Errorf(codes.Aborted, "Deleting %s stopped while %s. What's left of it is still there. Choose Delete Workspace… again to continue.",
			c.Name, deleteStepWords[c.Deleting.Step])
	}
	for {
		var err error
		switch c.Deleting.Step {
		case local.DeleteStepInstance:
			if err = d.RemoveInstance(ctx, c); err == nil {
				err = advance(local.DeleteStepHome)
			}
		case local.DeleteStepHome:
			if err = d.RemoveHome(ctx, c); err == nil {
				err = advance(local.DeleteStepSaves)
			}
		case local.DeleteStepSaves:
			if elsewhere {
				// The saves stay where they are, and so does the key.
				err = advance(local.DeleteStepRecord)
			} else if err = d.RemoveSaves(ctx, c); err == nil {
				s.log.Info("delete: saves deleted", "box_id", c.ID)
				err = advance(local.DeleteStepKey)
			}
		case local.DeleteStepKey:
			if !keysGone {
				return &daemonv1.DeleteBoxResponse{Step: daemonv1.DeleteStep_DELETE_STEP_KEY, DeleteKeyIds: local.KeyIDs(c)}, nil
			}
			err = advance(local.DeleteStepRecord)
		case local.DeleteStepRecord:
			if err = d.RemoveRecord(ctx, c); err != nil {
				break
			}
			if s.env.Provided != nil {
				s.env.Provided.Forget(local.KeyIDs(c)...)
			}
			s.mu.Lock()
			delete(s.elsewhere, c.Name)
			s.mu.Unlock()
			// Its team's state in memory goes too: a new workspace of the
			// same name starts with none.
			e := s.teamEng()
			e.mu.Lock()
			delete(e.boxes, c.Name)
			e.mu.Unlock()
			s.log.Info("deleted", "box_id", c.ID, "saves_left", elsewhere)
			return &daemonv1.DeleteBoxResponse{Finished: true, Step: daemonv1.DeleteStep_DELETE_STEP_RECORD, SavesLeftAt: c.Deleting.SavesLeftAt}, nil
		default:
			return nil, status.Errorf(codes.Internal, "Deleting %s stopped at a step Portenv doesn't know (%q).", c.Name, c.Deleting.Step)
		}
		if err != nil {
			return nil, stopped(err)
		}
	}
}

// engineDeleter deletes through this Mac's engine driver and folder.
type engineDeleter struct{ env *local.Env }

func (e engineDeleter) Running(ctx context.Context, c local.BoxConfig) (bool, error) {
	drv, err := e.env.BoxDriver()
	if err != nil {
		return false, err
	}
	defer func() { _ = drv.Close() }()
	id := driver.BoxID(c.ID)
	if !drv.Known(id) {
		return false, nil
	}
	st, err := drv.Stats(ctx, id)
	if err != nil {
		return false, err
	}
	return st.State == driver.StateRunning, nil
}

func (e engineDeleter) LeaseHolder(ctx context.Context, c local.BoxConfig) (string, error) {
	if repo, here := e.env.LocalRepository(c); here {
		if n, _, err := local.CountSaves(repo); err == nil && n == 0 {
			return "", nil // never saved: no save carries a lease
		}
	}
	sess, err := e.env.Open(c.Name)
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Drv.Close() }()
	ex := sess.HostExecutor()
	if ex == nil {
		return "", errors.New("restic isn't installed next to Portenv")
	}
	b, err := boxsync.Open(boxsync.Config{
		Executor: ex, MetaExecutor: ex, Repository: sess.HostRepo(), MetaRepository: sess.HostRepo(),
		Password: sess.Key, Env: sess.ResticEnv, SSHKey: sess.SSHKey, SSHHostKey: c.StorageHostKey,
		BoxID: c.ID, MachineID: e.env.Machine, HomeDir: "/nonexistent",
		StateDir: filepath.Join(e.env.Dir, "state", c.ID),
	})
	if err != nil {
		return "", err
	}
	l, err := b.Lease(ctx)
	if err != nil || l == nil {
		return "", err
	}
	return l.Machine, nil
}

func (e engineDeleter) RemoveInstance(ctx context.Context, c local.BoxConfig) error {
	drv, err := e.env.BoxDriver()
	if err != nil {
		return err
	}
	defer func() { _ = drv.Close() }()
	id := driver.BoxID(c.ID)
	if _, err := drv.Stop(ctx, id, 0); err != nil {
		return err
	}
	return drv.Destroy(ctx, id)
}

func (e engineDeleter) RemoveHome(ctx context.Context, c local.BoxConfig) error {
	drv, err := e.env.BoxDriver()
	if err != nil {
		return err
	}
	defer func() { _ = drv.Close() }()
	return drv.DeleteHome(ctx, driver.BoxID(c.ID), driver.HomeStorage{Ref: local.HomeVolume(c.ID)})
}

func (e engineDeleter) RemoveSaves(_ context.Context, c local.BoxConfig) error {
	return e.env.RemoveLocalRepository(c)
}

func (e engineDeleter) RemoveRecord(_ context.Context, c local.BoxConfig) error {
	if err := e.env.RemoveBoxState(c); err != nil {
		return err
	}
	return e.env.RemoveConfig(c)
}

// gitCall counts a push or fetch with GitHub in flight for the workspace
// until the returned func runs (R-0391): Delete Workspace waits for it.
func (s *Server) gitCall(name string) func() {
	s.mu.Lock()
	if s.gitBusy == nil {
		s.gitBusy = map[string]int{}
	}
	s.gitBusy[name]++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.gitBusy[name]--; s.gitBusy[name] <= 0 {
			delete(s.gitBusy, name)
		}
		s.mu.Unlock()
	}
}
