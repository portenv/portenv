// SPDX-License-Identifier: Apache-2.0

// Package daemon is portenvd: it owns the boxes open on this Mac and serves
// portenv.daemon.v1 to the app and the CLI over a Unix socket only this user
// can reach. Everything it does inside a box goes through the box agent's
// channel (ADR 0010), never docker exec.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/internal/version"
	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// SocketName is the daemon's socket in the Portenv directory.
const SocketName = "portenvd.sock"

// Server is portenvd's API.
type Server struct {
	daemonv1.UnimplementedDaemonServiceServer
	env *local.Env
	log *slog.Logger
	// pulling: the first open's image pull, for the box's state (R-0211).
	pulling pulls

	// relaunching: Portenv is relaunching for an update, so this daemon
	// stops without closing its boxes (ADR 0014); stop ends Serve.
	relaunching atomic.Bool
	stop        context.CancelFunc

	// peerUID reads a connection's peer uid (nil: the system's; tests set
	// another before Serve).
	peerUID func(fd int) (int, error)
	// identify is a connection's caller (nil: the owner; tests set another).
	identify func(net.Conn) caller

	// The background saves after Quit Anyway: they run until bgCtx ends
	// (portenvd stopping). sleep and closeForQuit are the clock and the
	// close (nil: the real ones; tests set fakes).
	bgCtx        context.Context
	bgCancel     context.CancelFunc
	sleep        func(ctx context.Context, d time.Duration) bool
	closeForQuit func(ctx context.Context, name string) error

	// team is the agent team (ADR 0022), made on first use; teamPumps
	// holds each workspace's TeamCalls stream.
	teamOnce  sync.Once
	team      *teamEngine
	teamMu    sync.Mutex
	teamPumps map[string]bool

	mu     sync.Mutex
	moves  map[string]string     // box name → the target of a Move To in progress
	open   map[string]*openBox   // by box name
	remote map[string]*remoteBox // boxes this Mac opened on a server
	// elsewhere: the lease another machine held on a box when OpenBox was
	// last refused (GetPlacement).
	elsewhere map[string]boxsync.Lease
	// deleting: boxes a DeleteBox call is deleting now (ADR 0029).
	deleting map[string]bool
	// gen counts each box's moves, closes and restarts by this daemon, so
	// a terminal that ends because of one is not reported as a failure.
	gen map[string]uint64

	// stopping: shutdown has begun (no new opens); opening: opens in
	// progress, which shutdown waits for.
	stopping bool
	opening  sync.WaitGroup

	network networkReport      // the app's last network report
	now     func() time.Time   // the clock (tests set it)
	alive   func(pid int) bool // whether a reporting app still runs
	// engine asks the box engine whether it answers (tests set it).
	engine func(ctx context.Context) error
	// freeBytes is the space free to this user on dir's volume (tests
	// set it).
	freeBytes func(dir string) (uint64, error)
	// githubAPI is GitHub's REST API, for a repository's size before it's
	// cloned (ADR 0025 §2); tests point it at their own server.
	githubAPI string
	// githubGit is GitHub's git over https (tests: their own server).
	githubGit string
	// pushes from workspaces waiting for the app (R-0391).
	pushOnce sync.Once
	pushes   *pushQuestions
	// gitBusy counts each workspace's pushes and fetches with GitHub in
	// flight (R-0391): Delete Workspace waits for them (ADR 0029).
	gitBusy map[string]int
	// deleter deletes boxes (ADR 0029; nil: this Mac's engine and folder,
	// tests set a fake).
	deleter boxDeleter
}

// openBox is a box this daemon opened: its session, sync engine and agent
// channel.
type openBox struct {
	mu sync.Mutex // one box operation at a time
	// closed: saved, released and stopped (set under mu); a close that
	// waited for that one finds nothing left to do.
	closed bool
	sess   *local.Session
	sb     *boxsync.Box
	conn   *grpc.ClientConn
	agent  agentv1.AgentServiceClient

	// What the save state is derived from besides the sync state.
	saving, offline, agentDown atomic.Bool
	// retrying: a restic run missed its deadline and is being retried;
	// failed: the last save failed (after its retry), until one succeeds.
	retrying, failed atomic.Bool
	// quitUnsaved: Portenv quit before this box could be saved; it is
	// being saved first thing (the quit marker).
	quitUnsaved atomic.Bool
	// background: left unsaved by Quit Anyway; portenvd retries the save
	// on its own until it works or the app takes the box back.
	background atomic.Bool
	// agentMu guards conn and agent for readers outside mu (the state
	// poll), so they never wait behind a save.
	agentMu sync.RWMutex
	// openedAt: when this portenvd opened the box (the inspector's "Since").
	openedAt time.Time
	// securityRestart: this open stopped and started the box again because
	// it was left running with mounts it shouldn't have (R-0201).
	securityRestart atomic.Bool
}

// takeOverDriver is what the take-over needs of a box's driver.
type takeOverDriver interface {
	AgentChannel(ctx context.Context, id driver.BoxID) (driver.AgentChannel, error)
	Rekey(ctx context.Context, id driver.BoxID) error
	Stop(ctx context.Context, id driver.BoxID, timeout time.Duration) (driver.State, error)
}

// takeOver handles a box left running without this process's channel
// secrets (portenvd restarted: a crash, an update). It is re-keyed in
// place, so it and its programs keep running (ADR 0014). If it was started
// with other mounts than it should have (every box's repositories, from
// before R-0201), Rekey refuses before making any key, and the box is
// stopped here so the open starts it again with only its own: that is a
// security restart, which the open reports to the person. Any other
// failure also stops it, to open a new channel. Its home stays (rule 3).
func (s *Server) takeOver(ctx context.Context, name string, d takeOverDriver, id driver.BoxID) (securityRestart bool, err error) {
	if _, err := d.AgentChannel(ctx, id); !errors.Is(err, driver.ErrNoChannel) {
		return false, nil
	}
	// Rekey refuses a box that isn't running; then it simply starts.
	switch err := d.Rekey(ctx, id); {
	case err == nil:
		s.log.Info("took over the running workspace (re-keyed, not restarted)", "box", name)
	case errors.Is(err, driver.ErrMountsChanged):
		// By workspace ID only: no name, no path.
		s.log.Warn("restarting the workspace to apply a security update: it could reach other workspaces' saves", "workspace", string(id))
		securityRestart = true
	default:
		s.log.Info("no running box to take over, or the re-key failed: starting it", "box", name, "err", err)
	}
	if _, err := d.AgentChannel(ctx, id); errors.Is(err, driver.ErrNoChannel) {
		s.leaving(name) // whatever had it open ends as for a restart
		if _, err := d.Stop(ctx, id, 0); err != nil {
			return false, err
		}
	}
	return securityRestart, nil
}

// New returns a daemon for this machine's Portenv directory.
func New(env *local.Env, log *slog.Logger) *Server {
	if env != nil && env.Provided == nil {
		env.Provided = keys.NewMemory()
	}
	if env != nil {
		// What portenvd and the runner say is read in the app.
		env.AppWords = true
	}
	bg, cancel := context.WithCancel(context.Background())
	return &Server{env: env, log: log, open: map[string]*openBox{}, moves: map[string]string{}, remote: map[string]*remoteBox{}, gen: map[string]uint64{},
		now: time.Now, alive: processAlive, bgCtx: bg, bgCancel: cancel,
		engine:    func(ctx context.Context) error { return env.EngineAnswers(ctx) },
		freeBytes: freeBytes}
}

// Serve listens on the socket in the Portenv directory (mode 0600 in a 0700
// directory) until ctx ends, then closes every box it opened (save and
// release).
func (s *Server) Serve(ctx context.Context) error {
	sock := filepath.Join(s.env.Dir, SocketName)
	if len(sock) >= 104 {
		return fmt.Errorf("socket path %s is longer than the 103 bytes Unix sockets allow; use a shorter PORTENV_HOME", sock)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.stop = cancel
	s.mu.Unlock()
	if err := privateDir(s.env.Dir); err != nil {
		return err
	}
	// One daemon per directory, before touching the socket: a second one
	// must not take the socket or any box from the first.
	lock, err := lockDir(s.env.Dir)
	if err != nil {
		return err
	}
	defer lock.release()
	_ = os.Remove(sock)
	lis, err := (&net.ListenConfig{}).Listen(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(sock) }()
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := s.grpcServer()
	daemonv1.RegisterDaemonServiceServer(srv, s)
	go func() {
		<-ctx.Done()
		// Leave every workspace running (R-0252, ADR 0023): a portenvd or
		// runner restart stops no one's work, and the next one takes each
		// workspace over with its programs. Then end every call, open
		// terminals included (a graceful stop would wait for terminals
		// forever). A relaunch for an update needs no quit marker.
		s.stopBackground()
		if !s.relaunching.Load() {
			s.shutdown()
		}
		srv.Stop()
	}()
	s.log.Info("serving", "socket", sock)
	peer := s.peerUID
	if peer == nil {
		peer = socketPeerUID
	}
	ownerName() // read once, at start
	err = srv.Serve(ownerOnly{Listener: lis, peerUID: peer, identify: s.identify})
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

// GetVersion implements daemonv1.DaemonServiceServer.
func (s *Server) GetVersion(context.Context, *daemonv1.GetVersionRequest) (*daemonv1.GetVersionResponse, error) {
	return &daemonv1.GetVersionResponse{Build: &typesv1.BuildInfo{Version: version.Version, Commit: version.Commit, BuildId: version.BuildID}}, nil
}

// engineTimeout bounds GetEngine: Welcome asks every 2 s.
const engineTimeout = 1500 * time.Millisecond

// GetEngine reports whether the box engine answers (Docker Desktop is
// running), for Welcome's Docker card and the state line (R-0098).
func (s *Server) GetEngine(ctx context.Context, _ *daemonv1.GetEngineRequest) (*daemonv1.GetEngineResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, engineTimeout)
	defer cancel()
	err := s.engine(ctx)
	if err != nil {
		s.log.Debug("engine check", "err", err)
	}
	return &daemonv1.GetEngineResponse{Running: err == nil}, nil
}

// ListBoxes lists the boxes configured on this machine.
func (s *Server) ListBoxes(context.Context, *daemonv1.ListBoxesRequest) (*daemonv1.ListBoxesResponse, error) {
	entries, err := os.ReadDir(filepath.Join(s.env.Dir, "boxes")) // workspace-word: ok (a path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	resp := &daemonv1.ListBoxesResponse{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		c, err := s.env.LoadBox(name)
		if err != nil {
			continue
		}
		state := typesv1.BoxState_BOX_STATE_STOPPED
		s.mu.Lock()
		if _, open := s.open[name]; open {
			state = typesv1.BoxState_BOX_STATE_RUNNING
		}
		s.mu.Unlock()
		st := &daemonv1.BoxStatus{Box: &typesv1.Box{Id: c.ID, Name: c.Name, ToolboxImage: c.Image}, State: state,
			CarriesRecoveryKey: c.Recovery}
		if c.Deleting != nil {
			st.Deleting, st.DeleteStep = true, deleteStepEnum[c.Deleting.Step]
		}
		resp.Boxes = append(resp.Boxes, st)
	}
	// Boxes CreateBox hasn't finished: shown, never opened (ADR 0019).
	creating, err := s.env.Creations()
	if err != nil {
		return nil, err
	}
	for _, c := range creating {
		// A crash between writing the config and removing the record
		// leaves both: the box is made (CreateBox again tidies up).
		if slices.ContainsFunc(resp.Boxes, func(b *daemonv1.BoxStatus) bool { return b.GetBox().GetId() == c.BoxID }) {
			continue
		}
		resp.Boxes = append(resp.Boxes, &daemonv1.BoxStatus{Box: &typesv1.Box{Id: c.BoxID, Name: c.Name, ToolboxImage: c.Image},
			State: typesv1.BoxState_BOX_STATE_STOPPED, Creating: true})
	}
	return resp, nil
}

// OpenBox applies the resume rules and starts the box on this Mac.
func (s *Server) OpenBox(ctx context.Context, req *daemonv1.OpenBoxRequest) (_ *daemonv1.OpenBoxResponse, err error) {
	// Logged by workspace ID, on entry and on every failure, never with a
	// path (R-0209, R-0211): an open that fails says why in the log, and
	// the app gets a plain line.
	id := s.boxID(req.GetName())
	s.log.Info("open", "box_id", id)
	defer func() {
		if err != nil {
			s.log.Warn("open failed", "box_id", id, "err", noPaths(err.Error()))
			err = plainOpenError(err)
		}
	}()
	if req.GetConnectThere() {
		return s.connectThere(ctx, req.GetName())
	}
	// Being deleted, or its deletion stopped part-way: its home or saves
	// may be gone, so it never starts (ADR 0029).
	if s.beingDeleted(req.GetName()) {
		return nil, status.Errorf(codes.FailedPrecondition, "Deleting %s stopped before it finished, so it can't open. Choose Delete Workspace… again to finish.", req.GetName())
	}
	// A box this Mac moved to a server stays there until Move To brings it
	// back: show it where it runs.
	if host := s.location(req.GetName()); host != "" {
		if st, err := remoteState(ctx, host, req.GetName()); err == nil && st.GetState() != daemonv1.SaveState_SAVE_STATE_CLOSED {
			s.attachRemote(req.GetName(), host)
			return &daemonv1.OpenBoxResponse{Summary: "open on " + host, Location: host}, nil
		}
		s.detachRemote(req.GetName())
	}
	s.mu.Lock()
	ob, open := s.open[req.GetName()]
	s.mu.Unlock()
	if open {
		if !ob.background.Load() {
			return &daemonv1.OpenBoxResponse{Summary: "already open"}, nil
		}
		// Left unsaved by Quit Anyway and still being saved in the
		// background: take it back as it is and save it first thing.
		if s.takeBackBox(ob) {
			go s.saveAfterQuit(req.GetName(), ob) // #nosec G118 -- the save outlives this open request on purpose
			return &daemonv1.OpenBoxResponse{Summary: "open on " + s.env.Machine + "; Portenv quit before saving it last time, saving now"}, nil
		}
		// Saved in the background meanwhile: it's closed; open it anew.
	}
	done, err := s.startOpening()
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, pulled := s.followPull(ctx, req.GetName())
	defer pulled()
	ob, res, err := s.openBox(ctx, req.GetName(), req.GetTakeOver())
	if err != nil {
		var held *boxsync.LeaseHeldError
		if errors.As(err, &held) {
			return nil, s.refuseOpenElsewhere(ctx, req.GetName(), held)
		}
		return nil, err
	}
	s.openedHere(req.GetName())
	ob.offline.Store(res.Offline)
	s.mu.Lock()
	s.open[req.GetName()] = ob
	s.mu.Unlock()
	summary := fmt.Sprintf("open on %s (resume rule %d: %s)", s.env.Machine, res.Rule, res.Action)
	// Rule 4: say where the unsaved work went, with its time.
	if o := res.Orphaned; o != nil {
		summary += fmt.Sprintf("; unsaved work from %s was kept as a separate save, %s (%s), before restoring the newer one",
			o.Machine, short8(o.ID), o.Time.Local().Format(time.DateTime))
	}
	if res.Offline {
		summary = "Offline · will save later"
	}
	// Portenv quit before this box was saved: say so, and save it first
	// thing (offline, the next save does it).
	if s.hasQuitMarker(ob.sess.Cfg.ID) {
		ob.quitUnsaved.Store(true)
		summary += "; Portenv quit before saving it last time, saving now"
		if !res.Offline {
			go s.saveAfterQuit(req.GetName(), ob) // #nosec G118 -- the save outlives this open request on purpose
		}
	}
	return &daemonv1.OpenBoxResponse{Summary: summary, Rule: int32(res.Rule)}, nil // #nosec G115 -- rules 1 to 5
}

func (s *Server) openBox(ctx context.Context, name string, takeOver bool) (*openBox, boxsync.ResumeResult, error) {
	var none boxsync.ResumeResult
	t0 := time.Now()
	sess, err := s.env.Open(name)
	if err != nil {
		return nil, none, keyError(err)
	}
	id := string(sess.ID())
	step := func(what string) { s.log.Info(what, "box_id", id, "after", time.Since(t0).Round(time.Millisecond)) }
	if err := sess.EnsureCreated(ctx, step); err != nil {
		return nil, none, err
	}
	// Storage reachability, alongside the box's start: no probe when the
	// app has just reported no network at all; otherwise a short probe.
	reach := make(chan bool, 1)
	if s.skipProbe() {
		step("no network (the app's report): offline without a probe")
		reach <- false
	} else {
		go func() {
			ok := sess.StorageReachable()
			step(fmt.Sprintf("storage probe: reachable=%v", ok))
			reach <- ok
		}()
	}

	restarted, err := s.takeOver(ctx, name, sess.Drv, sess.ID())
	if err != nil {
		return nil, none, err
	}
	if _, err := sess.Drv.Start(ctx, sess.ID()); err != nil {
		return nil, none, err
	}
	step("workspace started")
	ob := &openBox{sess: sess, openedAt: time.Now()}
	ob.securityRestart.Store(restarted)
	// An empty home makes the agent report FAILED; restic still runs.
	if _, err := ob.connect(ctx, true); err != nil {
		return nil, none, agentError(err)
	}
	step("workspace helper answers")
	// One sync engine for the whole open: its executor follows the box's
	// current channel, which changes when the box restarts below.
	if ob.sb, err = sess.SyncWith(boxsync.ChannelExecutor{Client: func() agentv1.AgentServiceClient { return ob.agent }}); err != nil {
		ob.stop(ctx)
		return nil, none, err
	}
	ob.sb.OnRetry(func(on bool) { ob.retrying.Store(on) })
	step("sync engine ready")
	var res boxsync.ResumeResult
	if !<-reach {
		if res, err = ob.sb.ResumeOffline(); err != nil {
			ob.stop(ctx)
			return nil, none, fmt.Errorf("storage is unreachable and %w", err)
		}
	} else {
		if err := ob.sb.Init(ctx); err != nil {
			ob.stop(ctx)
			return nil, none, err
		}
		if res, err = ob.sb.Resume(ctx, boxsync.ResumeOptions{TakeOver: takeOver}); err != nil {
			ob.stop(ctx)
			return nil, none, err
		}
	}
	step("resume rule decided")
	switch res.Action {
	case boxsync.ActionNewBox, boxsync.ActionRestored, boxsync.ActionRestoredKeptLocal:
		// The start sequence runs again on the new home: a fresh one from
		// the skeleton, or the one just restored.
		ob.closeConn()
		if err := sess.Restart(ctx, res.Action == boxsync.ActionNewBox); err != nil {
			return nil, none, err
		}
	}
	if state, err := ob.connect(ctx, false); err != nil {
		return nil, none, err
	} else if state != agentv1.ReadinessState_READINESS_STATE_READY {
		return nil, none, errors.New("the workspace failed to start")
	}
	step("workspace ready")
	return ob, res, nil
}

// connect opens the agent channel for the box's current start and waits
// until the agent reports READY (or FAILED, when allowFailed is set).
func (ob *openBox) connect(ctx context.Context, allowFailed bool) (agentv1.ReadinessState, error) {
	ob.closeConn()
	ch, err := ob.sess.Drv.AgentChannel(ctx, ob.sess.ID())
	if err != nil {
		return 0, err
	}
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		return 0, err
	}
	ob.agentMu.Lock()
	ob.conn, ob.agent = conn, agentv1.NewAgentServiceClient(conn)
	ob.agentMu.Unlock()
	deadline := time.Now().Add(15 * time.Minute)
	wait := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		r, err := ob.agent.GetReadiness(ctx, &agentv1.GetReadinessRequest{})
		if err != nil && strings.Contains(err.Error(), "the certificate is not this box's") { // workspace-word: ok (matches the agent's internal error)
			return 0, err // something else answers on the agent's port: never retry into it
		}
		if err == nil {
			switch r.GetState() {
			case agentv1.ReadinessState_READINESS_STATE_READY:
				return r.GetState(), nil
			case agentv1.ReadinessState_READINESS_STATE_FAILED:
				if allowFailed {
					return r.GetState(), nil
				}
				return r.GetState(), fmt.Errorf("the workspace failed to start: %s", r.GetDetail())
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(wait):
		}
		// At most 100 ms between asks: readiness is a cheap local call, and
		// a longer backoff left the terminal waiting up to a second after
		// the box was ready.
		wait = min(2*wait, 100*time.Millisecond)
	}
	return 0, errors.New("the workspace did not finish starting within 15 minutes")
}

// client is the box's current agent client, or nil (safe outside mu).
func (ob *openBox) client() agentv1.AgentServiceClient {
	ob.agentMu.RLock()
	defer ob.agentMu.RUnlock()
	return ob.agent
}

func (ob *openBox) closeConn() {
	ob.agentMu.Lock()
	defer ob.agentMu.Unlock()
	if ob.conn != nil {
		_ = ob.conn.Close()
		ob.conn, ob.agent = nil, nil
	}
}

func (ob *openBox) stop(ctx context.Context) {
	ob.closeConn()
	_, _ = ob.sess.Drv.Stop(ctx, ob.sess.ID(), 0)
}

func (s *Server) get(name string) (*openBox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ob, ok := s.open[name]
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is not open on this Mac", name)
	}
	return ob, nil
}

// CloseBox saves, releases and stops the box.
func (s *Server) CloseBox(ctx context.Context, req *daemonv1.CloseBoxRequest) (*daemonv1.CloseBoxResponse, error) {
	// Logged by workspace ID, as OpenBox is (ADR 0023): on entry, its save
	// and release, and every failure, never with a path.
	id := s.boxID(req.GetName())
	s.log.Info("close", "box_id", id)
	// Never in the middle of a Move To: the move closes and opens the
	// workspace itself (R-0249).
	if target, moving := s.moveTarget(req.GetName()); moving {
		s.log.Info("close refused", "box_id", id, "reason", "moving")
		_ = grpc.SetTrailer(ctx, metadata.Pairs(RefusalTrailer, RefusalMoving))
		return nil, status.Errorf(codes.FailedPrecondition, "%s is moving to %s. Close it once the move has finished.", req.GetName(), serverHost(target))
	}
	if err := s.closeBox(ctx, req.GetName()); err != nil {
		s.log.Warn("close failed", "box_id", id, "err", noPaths(err.Error()))
		return nil, err
	}
	return &daemonv1.CloseBoxResponse{}, nil
}

func (s *Server) closeBox(ctx context.Context, name string) error {
	ob, err := s.get(name)
	if err != nil {
		return err
	}
	s.leaving(name)
	ob.mu.Lock()
	defer ob.mu.Unlock()
	// Closed while this call waited (closing the last window, then the app
	// quitting): nothing left to do.
	if ob.closed {
		return nil
	}
	ob.saving.Store(true)
	snap, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SaveRelease})
	ob.saving.Store(false)
	ob.failed.Store(err != nil)
	if err != nil {
		return ob.noteErr(err)
	}
	if s.log != nil {
		s.log.Info("close saved", "box_id", string(ob.sess.ID()), "snapshot", short8(snap.ID))
	}
	ob.stop(ctx)
	ob.closed = true
	if s.log != nil {
		s.log.Info("close released", "box_id", string(ob.sess.ID()))
	}
	s.mu.Lock()
	delete(s.open, name)
	s.mu.Unlock()
	return nil
}

// MakeSavePoint saves the home as a save point.
func (s *Server) MakeSavePoint(ctx context.Context, req *daemonv1.MakeSavePointRequest) (*daemonv1.MakeSavePointResponse, error) {
	if out, err := s.onServer(ctx, req.GetName(), "point"); !errors.Is(err, errNotRemote) {
		if err != nil {
			return nil, err
		}
		return &daemonv1.MakeSavePointResponse{Snapshot: &typesv1.SnapshotRef{Id: strings.TrimPrefix(out, "save point ")}}, nil
	}
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.saving.Store(true)
	snap, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SavePoint})
	ob.saving.Store(false)
	ob.failed.Store(err != nil)
	if err != nil {
		return nil, ob.noteErr(err)
	}
	ob.offline.Store(false)
	return &daemonv1.MakeSavePointResponse{Snapshot: ref(ob.sess.Cfg.ID, snap)}, nil
}

// RevertToLastSavePoint saves the home, then restores the newest save
// point. Terminals keep running; their files change underneath them.
func (s *Server) RevertToLastSavePoint(ctx context.Context, req *daemonv1.RevertToLastSavePointRequest) (*daemonv1.RevertToLastSavePointResponse, error) {
	if out, err := s.onServer(ctx, req.GetName(), "revert"); !errors.Is(err, errNotRemote) {
		if err != nil {
			return nil, err
		}
		// "reverted to save point A; the work it replaced is save B; save
		// point time T" (the time since the UI compliance pass)
		var restored, before string
		_, _ = fmt.Sscanf(strings.ReplaceAll(out, ";", " "), "reverted to save point %s the work it replaced is save %s", &restored, &before)
		ref := &typesv1.SnapshotRef{Id: restored}
		if _, after, ok := strings.Cut(out, "save point time "); ok {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(after)); err == nil {
				ref.Time = timestamppb.New(t)
			}
		}
		return &daemonv1.RevertToLastSavePointResponse{Restored: ref, SavedBefore: &typesv1.SnapshotRef{Id: before}}, nil
	}
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.saving.Store(true)
	res, err := ob.sb.RevertToLastSavePoint(ctx)
	ob.saving.Store(false)
	ob.failed.Store(err != nil && !errors.Is(err, boxsync.ErrNoSavePoint))
	if errors.Is(err, boxsync.ErrNoSavePoint) {
		return nil, status.Error(codes.FailedPrecondition, "there is no save point yet")
	}
	if err != nil {
		return nil, ob.noteErr(err)
	}
	return &daemonv1.RevertToLastSavePointResponse{Restored: ref(ob.sess.Cfg.ID, res.Restored), SavedBefore: ref(ob.sess.Cfg.ID, res.SavedBefore)}, nil
}

// MoveBox closes the box where it is open and opens it on the target:
// "this-mac", or a server from the machine's servers list.
func (s *Server) MoveBox(ctx context.Context, req *daemonv1.MoveBoxRequest) (*daemonv1.MoveBoxResponse, error) {
	name, target := req.GetName(), req.GetTarget()
	defer s.moving(name, target)()
	s.leaving(name)
	if target == "this-mac" {
		// Close it on the server it runs on, through that server's runner
		// (save and release there): the one this Mac put it on, or the
		// known server another refusal named (2.3, Move it to this Mac).
		if host := s.holdingServer(ctx, name); host != "" {
			if _, err := local.AppOn(ctx, host, "close", name); err != nil && !strings.Contains(err.Error(), "is not open") {
				return nil, status.Error(codes.FailedPrecondition, err.Error())
			}
			s.detachRemote(name)
		}
		res, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: name})
		if err != nil {
			return nil, err
		}
		return &daemonv1.MoveBoxResponse{Summary: res.GetSummary()}, nil
	}
	servers, err := s.env.ServerTargets()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(servers, target) {
		return nil, status.Errorf(codes.InvalidArgument, "%s is not in this Mac's servers list", target)
	}
	// Its host key is confirmed, before anything here closes (R-0180).
	if err := s.env.CheckServer(target); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	c, err := s.env.LoadBox(name)
	if err != nil {
		return nil, err
	}
	if host := s.location(name); host != "" && host != target {
		if _, err := local.AppOn(ctx, host, "close", name); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		s.detachRemote(name)
	}
	if _, err := s.get(name); err == nil {
		if err := s.closeBox(ctx, name); err != nil {
			return nil, err
		}
	}
	out := logWriter{s.log.With("box", name, "target", target)}
	if !local.KnownOn(ctx, target, name) {
		if err := local.EnrolOn(ctx, s.env, c, target, joinStorage(c, target), out); err != nil {
			return nil, err
		}
	}
	// Opened there by the runner, with the agent channel (never docker exec).
	if _, err := local.AppOn(ctx, target, "open", name); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	s.attachRemote(name, target)
	return &daemonv1.MoveBoxResponse{Summary: "open on " + target}, nil
}

// joinStorage is where a server reaches the box's storage: itself, through
// host.portenv.internal, when the storage is on that server.
func joinStorage(c local.BoxConfig, target string) string {
	_, host, _ := strings.Cut(target, "@")
	if host == "" {
		host = target
	}
	if strings.HasPrefix(c.Storage, "sftp:") && strings.Contains(c.Storage, "@"+host+":") {
		return strings.Replace(c.Storage, "@"+host+":", "@host.portenv.internal:", 1)
	}
	return c.Storage
}

// Terminal relays a terminal to the box agent's Terminal call.
func (s *Server) Terminal(stream daemonv1.DaemonService_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "the first terminal message must be open")
	}
	gen := s.boxGen(open.GetBox())
	var ac agentv1.AgentServiceClient
	if s.location(open.GetBox()) != "" {
		if ac, err = s.remoteAgent(stream.Context(), open.GetBox()); err != nil {
			return err
		}
	} else {
		ob, err := s.get(open.GetBox())
		if err != nil {
			return err
		}
		ac = ob.agent
	}
	at, err := ac.Terminal(stream.Context())
	if err != nil {
		if s.location(open.GetBox()) != "" {
			s.dropRemoteAgent(open.GetBox())
		}
		return agentError(err)
	}
	size := &agentv1.TerminalSize{Cols: open.GetSize().GetCols(), Rows: open.GetSize().GetRows()}
	if err := at.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: open.GetSession(), Size: size, Viewer: open.GetViewer(), InputPiped: open.GetInputPiped()}}}); err != nil {
		return err
	}
	go func() {
		for {
			r, err := stream.Recv()
			if err != nil {
				_ = at.CloseSend()
				return
			}
			var m agentv1.TerminalRequest
			switch v := r.GetMsg().(type) {
			case *daemonv1.TerminalRequest_Input:
				m.Msg = &agentv1.TerminalRequest_Input{Input: v.Input}
			case *daemonv1.TerminalRequest_Resize:
				m.Msg = &agentv1.TerminalRequest_Resize{Resize: &agentv1.TerminalSize{Cols: v.Resize.GetCols(), Rows: v.Resize.GetRows()}}
			default:
				continue
			}
			if err := at.Send(&m); err != nil {
				return
			}
		}
	}()
	for {
		r, err := at.Recv()
		if err != nil {
			return s.terminalEnd(open.GetBox(), gen, err)
		}
		var m daemonv1.TerminalResponse
		switch v := r.GetMsg().(type) {
		case *agentv1.TerminalResponse_Output:
			m.Msg = &daemonv1.TerminalResponse_Output{Output: v.Output}
		case *agentv1.TerminalResponse_ExitCode:
			m.Msg = &daemonv1.TerminalResponse_ExitCode{ExitCode: v.ExitCode}
		}
		if err := stream.Send(&m); err != nil {
			return err
		}
	}
}

func ref(boxID string, s boxsync.Snapshot) *typesv1.SnapshotRef {
	return &typesv1.SnapshotRef{Id: s.ID, BoxId: boxID, Time: timestamppb.New(s.Time), MachineId: s.Machine}
}

// logWriter turns remote command output into log lines.
type logWriter struct{ log *slog.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.log.Info(line)
		}
	}
	return len(p), nil
}

// moving records a Move To in progress until the returned func runs: a
// close (Close Workspace, Quit) is refused meanwhile.
func (s *Server) moving(name, target string) (done func()) {
	s.mu.Lock()
	s.moves[name] = target
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.moves, name)
		s.mu.Unlock()
	}
}

// moveTarget is where a Move To in progress is taking the box.
func (s *Server) moveTarget(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.moves[name]
	return t, ok
}

// serverHost is a move target as people read it: "test-server" for
// "dev@test-server", "this Mac" for this-mac.
func serverHost(target string) string {
	if target == "this-mac" {
		return "this Mac"
	}
	if _, host, ok := strings.Cut(target, "@"); ok {
		return host
	}
	return target
}

// leaving records that this daemon is moving, closing or restarting the
// box: its open terminals are about to end, and that is not a failure.
func (s *Server) leaving(name string) {
	s.mu.Lock()
	s.gen[name]++
	s.mu.Unlock()
}

// boxGen is the box's count of moves, closes and restarts so far.
func (s *Server) boxGen(name string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen[name]
}

// terminalEnd is how a terminal opened at gen ends after err from the
// agent: quietly when the shell ended or the box was moved, closed or
// restarted since (the app reconnects it), otherwise as agentError.
func (s *Server) terminalEnd(name string, gen uint64, err error) error {
	if errors.Is(err, io.EOF) || s.boxGen(name) != gen {
		return nil
	}
	return agentError(err)
}

// agentError reports a channel that is down or fails verification (for
// example something else answering on the agent's port) as the box agent
// being unavailable: nothing was sent to it.
func agentError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if status.Code(err) == codes.Unavailable || strings.Contains(msg, "the certificate is not this box's") { // workspace-word: ok (matches the agent's internal error)
		return status.Error(codes.Unavailable, AgentUnavailable)
	}
	return err
}

// AgentUnavailable is what portenvd says when the box agent's channel is
// down (Unavailable): portenvd itself is fine.
const AgentUnavailable = "the workspace helper is unavailable (its channel is down or failed verification); restart the workspace"

// CheckBox reports whether the box agent answers on its channel.
func (s *Server) CheckBox(ctx context.Context, req *daemonv1.CheckBoxRequest) (*daemonv1.CheckBoxResponse, error) {
	name := req.GetName()
	if _, err := s.onServer(ctx, name, "check"); !errors.Is(err, errNotRemote) {
		if err != nil {
			return &daemonv1.CheckBoxResponse{Detail: status.Convert(err).Message()}, nil
		}
		return &daemonv1.CheckBoxResponse{AgentAvailable: true}, nil
	}
	ob, err := s.get(name)
	if err != nil {
		// Not open in this daemon (for example a runner that restarted)
		// but still running: its channel's secrets are gone.
		if sess, err2 := s.env.Open(name); err2 == nil && sess.Drv.Known(sess.ID()) {
			if st, err3 := sess.Drv.Stats(ctx, sess.ID()); err3 == nil && st.State == driver.StateRunning {
				return &daemonv1.CheckBoxResponse{Detail: "the workspace helper is unavailable (the workspace runs without a channel); restart the workspace"}, nil
			}
		}
		return nil, err
	}
	if ob.agent == nil {
		ob.agentDown.Store(true)
		return &daemonv1.CheckBoxResponse{Detail: "the workspace helper is unavailable"}, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := ob.agent.GetReadiness(cctx, &agentv1.GetReadinessRequest{}); err != nil {
		return &daemonv1.CheckBoxResponse{Detail: status.Convert(ob.noteErr(err)).Message()}, nil
	}
	ob.agentDown.Store(false)
	return &daemonv1.CheckBoxResponse{AgentAvailable: true}, nil
}

// RestartBox stops the box without saving (with its agent gone, nothing in
// it can be saved) and opens it again through the resume rules. Its home
// stays on its volume and this machine's state still says it has unsaved
// changes, so nothing is restored over it (rule 3 on this machine); the
// next save includes everything.
func (s *Server) RestartBox(ctx context.Context, req *daemonv1.RestartBoxRequest) (*daemonv1.RestartBoxResponse, error) {
	name := req.GetName()
	s.leaving(name)
	if out, err := s.onServer(ctx, name, "restart"); !errors.Is(err, errNotRemote) {
		if err != nil {
			return nil, err
		}
		s.dropRemoteAgent(name) // a new start: new secrets
		return &daemonv1.RestartBoxResponse{Summary: strings.TrimPrefix(out, "restarted: ")}, nil
	}
	// A box this daemon does not hold open (for example after a runner
	// restart) is reopened the usual way, which restarts it if it runs
	// without a channel; the resume rules keep the local home (rule 3).
	if ob, err := s.get(name); err == nil {
		ob.mu.Lock()
		ob.stop(ctx)
		s.mu.Lock()
		delete(s.open, name)
		s.mu.Unlock()
		ob.mu.Unlock()
	}
	res, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RestartBoxResponse{Summary: res.GetSummary(), Rule: res.GetRule()}, nil
}

// noteErr reports an action's error, recording a channel that is down so
// the save state says so.
func (ob *openBox) noteErr(err error) error {
	err = agentError(err)
	if status.Code(err) == codes.Unavailable {
		ob.agentDown.Store(true)
	}
	return err
}

// short8 is a save's short ID.
func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// boxID is a workspace's ID for the log, or "" when its settings can't be
// read (the failure says why).
func (s *Server) boxID(name string) string {
	c, err := s.env.LoadBox(name)
	if err != nil {
		return ""
	}
	return c.ID
}

// The lines the app shows when the container engine isn't running, doesn't
// answer, or a download stops (R-0211), worded as the app's own engine lines
// (DaemonErrorText), which pass them through. The detail goes to the log.
// testdata/engine-lines.txt holds the same three: the Go test checks these
// against it, and the app's test runs each through DaemonErrorText
// unchanged (R-0214).
const (
	EngineNotRunning   = "Portenv uses Docker Desktop to run workspaces on this Mac. Start Docker Desktop, then click Try Again."
	EngineUnresponsive = "Docker Desktop is still starting, or isn’t answering. Wait a moment, then click Try Again."
	PullStalled        = "Portenv couldn’t get the workspace image: the download stopped. Check this Mac is online, then click Try Again."
)

// plainOpenError turns the engine's failures into their plain lines; every
// other error is already the one the app shows.
func plainOpenError(err error) error {
	switch {
	case errors.Is(err, driver.ErrPullStalled):
		return status.Error(codes.Unavailable, PullStalled)
	case errors.Is(err, driver.ErrEngineNotRunning):
		return status.Error(codes.Unavailable, EngineNotRunning)
	case errors.Is(err, driver.ErrEngineUnresponsive):
		return status.Error(codes.Unavailable, EngineUnresponsive)
	}
	return err
}

// pathRE is a file path of two or more parts, absolute or from ~; a part
// may hold spaces ("Application Support").
var pathRE = regexp.MustCompile(`(^|[\s"'(=])~?/(?:[^/\s"'()]+(?: [^/\s"'()]+)*/)+[^\s"'():]*`)

// noPaths hides the file paths in a log line: they can name the person (a
// home folder) and say nothing a workspace ID doesn't.
func noPaths(s string) string {
	return pathRE.ReplaceAllString(s, "${1}…")
}
