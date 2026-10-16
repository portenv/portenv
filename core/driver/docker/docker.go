// SPDX-License-Identifier: Apache-2.0

// Package docker implements the box driver on the Docker Engine API. It is
// the Phase 0 driver and the fallback on Macs without Apple Containerization.
//
// Each box is one container named portenv-<box-id>. Its root filesystem is
// disposable: Start always creates a fresh container from the box's spec, so
// nothing written outside /home survives a restart (only /home travels, and
// tampering with image files never outlives a session; ADR 0005). The spec
// lives in the driver's state directory; /home is a named volume.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

const (
	labelBox   = "dev.portenv.box"
	namePrefix = "portenv-"
	// StorageMount is where local storage appears inside a box: each box's
	// own repository folder, at Config.StorageRepo under it.
	StorageMount = agent.StorageMount
	defaultStop  = 10 * time.Second
	defaultShm   = 1 << 30
)

// Config configures the driver.
type Config struct {
	// StateDir holds each box's spec. Required.
	StateDir string
	// StorageDir, when set, is the box's own repository folder on this
	// machine (local storage), bind-mounted at StorageRepo so restic inside
	// the box can reach it. Only the box's own: never the folder holding
	// every box's repositories, which would let one workspace delete
	// another's saves (R-0201).
	StorageDir string
	// StorageRepo is where StorageDir appears inside the box, one or two
	// levels under StorageMount (StorageMount/<box-id> or
	// StorageMount/boxes/<box-id>). Required with StorageDir.
	StorageRepo string
	// Client overrides the Docker client; nil means one from the
	// environment (DOCKER_HOST and friends).
	Client *client.Client
	// Namespace, when set, is added to container names so several simulated
	// machines can share one engine (tests only).
	Namespace string
	// HomesDir, when set, is where home volumes live on the host: each named
	// volume is a bind of HomesDir/<volume>. On servers this is the LUKS
	// volume for box homes (milestone 0.5); unset, Docker's default storage
	// is used (on a Mac, inside Docker's disk on a FileVault-encrypted drive).
	HomesDir string
}

// EngineWait is how long one engine call may go unanswered, and how long an
// image download may go without progress, before it fails with
// driver.ErrEngineUnresponsive or driver.ErrPullStalled (R-0211). A pull
// that keeps progressing may take as long as it needs. Tests shorten it.
var EngineWait = 30 * time.Second

// Driver is the docker box driver.
type Driver struct {
	cfg Config
	cli *client.Client

	mu       sync.Mutex
	channels map[driver.BoxID]agent.ChannelSecrets // this process's starts only
}

var _ driver.Driver = (*Driver)(nil)

// New returns a driver connected to the Docker engine.
func New(cfg Config) (*Driver, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("docker driver state: %w", err)
	}
	cli := cfg.Client
	if cli == nil {
		var err error
		cli, err = client.New(client.FromEnv)
		if err != nil {
			return nil, fmt.Errorf("connect to Docker: %w", err)
		}
	}
	return &Driver{cfg: cfg, cli: cli}, nil
}

// check refuses a configuration that would mount more than a box's own.
func (c Config) check() error {
	if c.StateDir == "" {
		return errors.New("docker driver: StateDir is required")
	}
	if c.StorageDir == "" {
		return nil
	}
	rel, ok := strings.CutPrefix(c.StorageRepo, StorageMount+"/")
	if !ok || path.Clean(c.StorageRepo) != c.StorageRepo {
		return fmt.Errorf("docker driver: the storage repository %q isn't one workspace's folder under %s", c.StorageRepo, StorageMount)
	}
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 1 && idRE.MatchString(parts[0]) && parts[0] != "boxes": // workspace-word: ok (a path)
	case len(parts) == 2 && parts[0] == "boxes" && idRE.MatchString(parts[1]): // workspace-word: ok (a path)
	default:
		return fmt.Errorf("docker driver: the storage repository %q isn't one workspace's folder under %s", c.StorageRepo, StorageMount)
	}
	return nil
}

// spec is what the driver remembers about a box between calls.
type spec struct {
	Box  driver.Box         `json:"box"`
	Home driver.HomeStorage `json:"home"`
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (d *Driver) specPath(id driver.BoxID) (string, error) {
	if !idRE.MatchString(string(id)) {
		return "", fmt.Errorf("invalid workspace ID %q", id)
	}
	return filepath.Join(d.cfg.StateDir, string(id)+".json"), nil
}

func (d *Driver) loadSpec(id driver.BoxID) (spec, error) {
	p, err := d.specPath(id)
	if err != nil {
		return spec{}, err
	}
	b, err := os.ReadFile(p) // #nosec G304 -- validated box ID in the driver's state dir
	if errors.Is(err, os.ErrNotExist) {
		return spec{}, fmt.Errorf("workspace %s does not exist", id)
	}
	if err != nil {
		return spec{}, err
	}
	var s spec
	return s, json.Unmarshal(b, &s)
}

func (d *Driver) saveSpec(s spec) error {
	p, err := d.specPath(s.Box.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (d *Driver) containerName(id driver.BoxID) string {
	if d.cfg.Namespace != "" {
		return namePrefix + d.cfg.Namespace + "-" + string(id)
	}
	return namePrefix + string(id)
}

// Create records the box and creates its container from the toolbox image,
// pulling the image if it is not present.
func (d *Driver) Create(ctx context.Context, box driver.Box) (driver.State, error) {
	if _, err := d.specPath(box.ID); err != nil {
		return driver.StateUnspecified, err
	}
	if err := d.ensureImage(ctx, box); err != nil {
		return driver.StateUnspecified, err
	}
	s := spec{Box: box}
	if err := d.saveSpec(s); err != nil {
		return driver.StateUnspecified, err
	}
	if err := d.recreate(ctx, s); err != nil {
		return driver.StateUnspecified, err
	}
	return driver.StateCreated, nil
}

// call is the context for one engine call: it ends after EngineWait (plus
// extra, for a call that waits on purpose, like a stop's grace period).
func call(ctx context.Context, extra time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, EngineWait+extra)
}

// engineErr says what kind of failure an engine call's error is: an engine
// nothing answers for is driver.ErrEngineNotRunning; one that didn't answer
// in time, or is still starting, is driver.ErrEngineUnresponsive (each kept
// with its detail); any other error is returned as it is. parent is the
// caller's context: its own end isn't the engine's fault.
func engineErr(parent context.Context, what string, err error) error {
	if err == nil {
		return nil
	}
	if parent.Err() != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if client.IsErrConnectionFailed(err) {
		return fmt.Errorf("%s: %w: %w", what, driver.ErrEngineNotRunning, err)
	}
	// Docker Desktop still starting answers with its own deadline
	// ("dialing …: context deadline exceeded"), not a code.
	if errors.Is(err, context.DeadlineExceeded) || cerrdefs.IsUnavailable(err) || cerrdefs.IsDeadlineExceeded(err) ||
		strings.Contains(err.Error(), "deadline exceeded") {
		return fmt.Errorf("%s: %w: %w", what, driver.ErrEngineUnresponsive, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (d *Driver) ensureImage(ctx context.Context, box driver.Box) error {
	ref := box.ToolboxImage
	cctx, cancel := call(ctx, 0)
	_, err := d.cli.ImageInspect(cctx, ref)
	cancel()
	if err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return engineErr(ctx, "inspect image "+ref, err)
	}
	if box.Progress != nil {
		box.Progress("pulling the toolbox image")
	}
	return d.pull(ctx, ref)
}

// pull downloads ref for as long as it keeps progressing: it fails with
// driver.ErrPullStalled once EngineWait passes with no message from the
// engine (the request itself included).
func (d *Driver) pull(ctx context.Context, ref string) error {
	pctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	wait := EngineWait
	progress := make(chan struct{}, 1)
	done, watched := make(chan struct{}), make(chan struct{})
	defer func() { close(done); <-watched }()
	go func() {
		defer close(watched)
		t := time.NewTimer(wait)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-progress:
				t.Reset(wait)
			case <-t.C:
				stop(driver.ErrPullStalled)
				return
			}
		}
	}()
	stalled := func(err error) error {
		if errors.Is(context.Cause(pctx), driver.ErrPullStalled) {
			return fmt.Errorf("pull %s: %w", ref, driver.ErrPullStalled)
		}
		return engineErr(ctx, "pull "+ref, err)
	}
	resp, err := d.cli.ImagePull(pctx, ref, client.ImagePullOptions{})
	if err != nil {
		return stalled(err)
	}
	for m, err := range resp.JSONMessages(pctx) {
		if err != nil {
			return stalled(err)
		}
		if m.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, m.Error.Message)
		}
		select {
		case progress <- struct{}{}:
		default:
		}
	}
	if err := context.Cause(pctx); err != nil {
		return stalled(err)
	}
	return nil
}

// recreate replaces the box's container with a fresh one from its spec.
// The box must not be running.
func (d *Driver) recreate(ctx context.Context, s spec) error {
	name := d.containerName(s.Box.ID)
	cctx, cancel := call(ctx, 0)
	_, err := d.cli.ContainerRemove(cctx, name, client.ContainerRemoveOptions{})
	cancel()
	if err != nil && !cerrdefs.IsNotFound(err) {
		return engineErr(ctx, "remove old container", err)
	}
	env := []string{agent.ChannelEnv + "=stdin", agent.BoxIDEnv + "=" + string(s.Box.ID)}
	if s.Home.Fresh {
		env = append(env, "PORTENV_INIT_HOME=1")
	}
	host := d.hostConfig(s)
	cctx, cancel = call(ctx, 0)
	defer cancel()
	_, err = d.cli.ContainerCreate(cctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        s.Box.ToolboxImage,
			Hostname:     driver.Hostname(s.Box.Name, s.Box.ID),
			Env:          env,
			Labels:       map[string]string{labelBox: string(s.Box.ID)},
			ExposedPorts: network.PortSet{agentPort: {}},
			// The agent channel's secrets arrive on stdin (ADR 0010). The
			// engine keeps stdin open across attaches (StdinOnce off), so a
			// restarted daemon can re-key the running box (ADR 0014).
			OpenStdin: true, StdinOnce: false, AttachStdin: true,
		},
		HostConfig: host,
	})
	if err != nil {
		return engineErr(ctx, "create container", err)
	}
	return nil
}

// hostConfig is the container's host configuration. It never makes the box
// privileged, never adds capabilities, never shares the host's PID, IPC or
// network namespaces and never relaxes seccomp or AppArmor: root in the box
// must not be able to read another process's memory (ADR 0005, conditions).
// TestHostConfigKeepsIsolation enforces this.
//
// Changing the mounts restarts every box left running across the update,
// once: Rekey compares a running box's mounts with these and refuses on
// any difference (driver.ErrMountsChanged), and the open then stops and
// starts it with the security-update notice (R-0201). A change made for
// another reason must give the notice that reason (R-0210).
func (d *Driver) hostConfig(s spec) *container.HostConfig {
	shm := int64(s.Box.Resources.SharedMemoryBytes) // #nosec G115 -- sizes far below MaxInt64
	if shm == 0 {
		shm = defaultShm
	}
	host := &container.HostConfig{
		ShmSize: shm,
		// The machine running the box, for storage on that machine (SFTP)
		// and later the port relay's way back to the Mac.
		ExtraHosts: []string{"host.portenv.internal:host-gateway"},
		Resources: container.Resources{
			NanoCPUs: int64(s.Box.Resources.CPUMillis) * 1_000_000,
			Memory:   int64(s.Box.Resources.MemoryBytes), // #nosec G115 -- sizes far below MaxInt64
		},
		// The agent channel (ADR 0010), on this machine's loopback address
		// only, at a port the engine picks. Nothing else is published.
		PortBindings: network.PortMap{agentPort: {{HostIP: netip.AddrFrom4([4]byte{127, 0, 0, 1}), HostPort: ""}}},
	}
	if s.Home.Ref != "" {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: s.Home.Ref, Target: "/home"})
	}
	if d.cfg.StorageDir != "" {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: d.cfg.StorageDir, Target: d.cfg.StorageRepo})
	}
	// restic's cache survives restarts (the root file system does not), so
	// a same-machine resume reads almost nothing remotely. It holds only
	// encrypted repository data and is never saved or moved.
	host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: d.cacheVolume(s.Box.ID), Target: CacheMount})
	return host
}

// engineSocket is whether p names a container engine's control socket,
// which no box may ever have: it is root on the machine.
func engineSocket(p string) bool {
	return agent.EngineSocket(p)
}

// sameMounts is whether a running container's mounts are the ones its
// spec gives now, in any order. A bind mount's source is matched as the
// engine reports it (sameBindSource).
func sameMounts(want, got []mount.Mount) bool {
	if len(want) != len(got) {
		return false
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for i, g := range got {
			if !used[i] && sameMount(w, g) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameMount(want, got mount.Mount) bool {
	if want.Type != got.Type || want.Target != got.Target || want.ReadOnly != got.ReadOnly {
		return false
	}
	if want.Type == mount.TypeBind {
		return sameBindSource(want.Source, got.Source)
	}
	return want.Source == got.Source
}

// desktopHostMount is where Docker Desktop's VM sees the Mac's files.
const desktopHostMount = "/host_mnt"

// sameBindSource is whether the engine's report of a bind mount's source is
// the folder the driver mounted. Docker Engine reports it as given. Docker
// Desktop reports it as /host_mnt plus the real path, with symlinks
// resolved (/var is /private/var on a Mac), so a plain comparison found
// every box "changed" after a portenvd restart and restarted it (R-0242).
func sameBindSource(want, got string) bool {
	if got == want {
		return true
	}
	rest, ok := strings.CutPrefix(got, desktopHostMount+"/")
	if !ok {
		return false
	}
	rest = "/" + rest
	if rest == want {
		return true
	}
	real, err := filepath.EvalSymlinks(want)
	return err == nil && rest == real
}

// CacheMount is restic's cache inside every box (portenv-sync's, 0700).
const CacheMount = "/var/cache/portenv-sync"

func (d *Driver) cacheVolume(id driver.BoxID) string {
	if d.cfg.Namespace != "" {
		return "portenv-cache-" + d.cfg.Namespace + "-" + string(id)
	}
	return "portenv-cache-" + string(id)
}

// MountHome records the box's home storage, creating the named volume if
// needed, and recreates the stopped container with it mounted at /home.
func (d *Driver) MountHome(ctx context.Context, id driver.BoxID, home driver.HomeStorage) error {
	s, err := d.loadSpec(id)
	if err != nil {
		return err
	}
	if running, err := d.running(ctx, id); err != nil {
		return err
	} else if running {
		return errors.New("MountHome needs the box stopped") // workspace-word: ok (programming error)
	}
	vctx, cancel := call(ctx, 0)
	_, verr := d.cli.VolumeInspect(vctx, home.Ref, client.VolumeInspectOptions{})
	cancel()
	if cerrdefs.IsNotFound(verr) {
		opts := client.VolumeCreateOptions{Name: home.Ref, Labels: map[string]string{labelBox: string(id)}}
		if d.cfg.HomesDir != "" {
			if !idRE.MatchString(home.Ref) {
				return fmt.Errorf("invalid home volume name %q", home.Ref)
			}
			dir := filepath.Join(d.cfg.HomesDir, home.Ref)
			if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- becomes /home inside the box; homes in it are 0700
				return fmt.Errorf("create home directory: %w", err)
			}
			opts.Driver = "local"
			opts.DriverOpts = map[string]string{"type": "none", "o": "bind", "device": dir}
		}
		vctx, cancel := call(ctx, 0)
		_, err := d.cli.VolumeCreate(vctx, opts)
		cancel()
		if err != nil {
			return engineErr(ctx, "create home volume", err)
		}
	} else if verr != nil {
		return engineErr(ctx, "inspect home volume", verr)
	}
	s.Home = home
	if err := d.saveSpec(s); err != nil {
		return err
	}
	return d.recreate(ctx, s)
}

// Start starts the box in a fresh container from its spec.
func (d *Driver) Start(ctx context.Context, id driver.BoxID) (driver.State, error) {
	s, err := d.loadSpec(id)
	if err != nil {
		return driver.StateUnspecified, err
	}
	if running, err := d.running(ctx, id); err != nil {
		return driver.StateUnspecified, err
	} else if running {
		return driver.StateRunning, nil
	}
	if err := d.recreate(ctx, s); err != nil {
		return driver.StateUnspecified, err
	}
	sec, err := agent.NewChannelSecrets()
	if err != nil {
		return driver.StateUnspecified, err
	}
	// The secrets go to the box's stdin (ADR 0010): attach before starting,
	// write one line, close. Never a file, never exec.
	att, err := d.cli.ContainerAttach(ctx, d.containerName(id), client.ContainerAttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return driver.StateUnspecified, fmt.Errorf("attach the agent channel: %w", err)
	}
	defer att.Close()
	d.mu.Lock()
	if d.channels == nil {
		d.channels = map[driver.BoxID]agent.ChannelSecrets{}
	}
	d.channels[id] = sec
	d.mu.Unlock()
	sctx, cancel := call(ctx, 0)
	_, err = d.cli.ContainerStart(sctx, d.containerName(id), client.ContainerStartOptions{})
	cancel()
	if err != nil {
		return driver.StateFailed, engineErr(ctx, "start container", err)
	}
	line, err := sec.Line()
	if err != nil {
		return driver.StateFailed, err
	}
	if _, err := att.Conn.Write(line); err != nil {
		return driver.StateFailed, fmt.Errorf("hand the agent channel its secrets: %w", err)
	}
	// The attach closes when Start returns; the container's stdin stays
	// open (StdinOnce off) for a later re-key.
	// A fresh home is created once; later starts must find it in place.
	if s.Home.Fresh {
		s.Home.Fresh = false
		if err := d.saveSpec(s); err != nil {
			return driver.StateRunning, err
		}
	}
	return driver.StateRunning, nil
}

// Stop stops the box, forcing it after timeout.
func (d *Driver) Stop(ctx context.Context, id driver.BoxID, timeout time.Duration) (driver.State, error) {
	if timeout <= 0 {
		timeout = defaultStop
	}
	secs := int(timeout.Round(time.Second) / time.Second)
	sctx, cancel := call(ctx, time.Duration(secs)*time.Second)
	defer cancel()
	if _, err := d.cli.ContainerStop(sctx, d.containerName(id), client.ContainerStopOptions{Timeout: &secs}); err != nil && !cerrdefs.IsNotFound(err) {
		return driver.StateUnspecified, engineErr(ctx, "stop container", err)
	}
	return driver.StateStopped, nil
}

// Destroy removes the box's container and spec. The home volume is kept.
func (d *Driver) Destroy(ctx context.Context, id driver.BoxID) error {
	if _, err := d.cli.ContainerRemove(ctx, d.containerName(id), client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	p, err := d.specPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Logs yields the box's console output.
func (d *Driver) Logs(ctx context.Context, id driver.BoxID, opts driver.LogOptions) iter.Seq2[driver.LogChunk, error] {
	return func(yield func(driver.LogChunk, error) bool) {
		o := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: opts.Follow, Timestamps: true}
		if !opts.Since.IsZero() {
			o.Since = strconv.FormatInt(opts.Since.Unix(), 10)
		}
		if opts.TailLines > 0 {
			o.Tail = strconv.Itoa(opts.TailLines)
		}
		rc, err := d.cli.ContainerLogs(ctx, d.containerName(id), o)
		if err != nil {
			yield(driver.LogChunk{}, fmt.Errorf("container logs: %w", err))
			return
		}
		defer func() { _ = rc.Close() }()
		chunks := make(chan driver.LogChunk)
		done := make(chan error, 1)
		go func() {
			_, err := stdcopy.StdCopy(chanWriter{chunks, driver.LogStreamStdout, ctx}, chanWriter{chunks, driver.LogStreamStderr, ctx}, rc)
			close(chunks)
			done <- err
		}()
		for chunk := range chunks {
			if !yield(chunk, nil) {
				_ = rc.Close()
				for range chunks { // drain so the copier can finish
				}
				return
			}
		}
		if err := <-done; err != nil && ctx.Err() == nil {
			yield(driver.LogChunk{}, fmt.Errorf("container logs: %w", err))
		}
	}
}

// chanWriter turns each demultiplexed write into a LogChunk, splitting off
// Docker's timestamp prefix.
type chanWriter struct {
	ch     chan<- driver.LogChunk
	stream driver.LogStream
	ctx    context.Context
}

func (w chanWriter) Write(p []byte) (int, error) {
	chunk := driver.LogChunk{Stream: w.stream, Data: bytes.Clone(p)}
	if ts, rest, ok := strings.Cut(string(p), " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			chunk.Time, chunk.Data = t, []byte(rest)
		}
	}
	select {
	case w.ch <- chunk:
		return len(p), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

// Stats returns a point-in-time snapshot of the box.
func (d *Driver) Stats(ctx context.Context, id driver.BoxID) (driver.Stats, error) {
	insp, err := d.cli.ContainerInspect(ctx, d.containerName(id), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return driver.Stats{State: driver.StateStopped, Time: time.Now()}, nil
	}
	if err != nil {
		return driver.Stats{}, fmt.Errorf("inspect container: %w", err)
	}
	st := driver.Stats{State: stateOf(insp.Container.State), Time: time.Now()}
	if st.State != driver.StateRunning {
		return st, nil
	}
	res, err := d.cli.ContainerStats(ctx, d.containerName(id), client.ContainerStatsOptions{Stream: false})
	if err != nil {
		return driver.Stats{}, fmt.Errorf("container stats: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return driver.Stats{}, fmt.Errorf("decode stats: %w", err)
	}
	st.Time = s.Read
	st.CPUTime = time.Duration(s.CPUStats.CPUUsage.TotalUsage) // #nosec G115 -- nanoseconds
	st.MemoryBytes = s.MemoryStats.Usage
	st.MemoryLimitBytes = s.MemoryStats.Limit
	if insp.Container.HostConfig != nil && insp.Container.HostConfig.Memory == 0 {
		st.MemoryLimitBytes = 0 // the engine reports the VM's memory when unlimited
	}
	st.ProcessCount = int(s.PidsStats.Current) // #nosec G115 -- process counts are small
	return st, nil
}

func stateOf(s *container.State) driver.State {
	if s == nil {
		return driver.StateUnspecified
	}
	switch {
	case s.Running:
		return driver.StateRunning
	case s.Restarting:
		return driver.StateStarting
	case s.Status == container.StateCreated:
		return driver.StateCreated
	case s.OOMKilled || (s.ExitCode != 0 && s.ExitCode != 143 && s.ExitCode != 137):
		return driver.StateFailed
	default:
		return driver.StateStopped
	}
}

// SetResources records new settings. CPU and memory apply to a running box
// at once; shared memory, Docker in the box and GPU need a restart.
func (d *Driver) SetResources(ctx context.Context, id driver.BoxID, r driver.Resources) (bool, error) {
	s, err := d.loadSpec(id)
	if err != nil {
		return false, err
	}
	if r.DockerInBox || r.GPU {
		return false, errors.New("docker driver: Docker in the workspace and GPU are not supported yet")
	}
	old := s.Box.Resources
	s.Box.Resources = r
	if err := d.saveSpec(s); err != nil {
		return false, err
	}
	running, err := d.running(ctx, id)
	if err != nil || !running {
		return false, err
	}
	if r.CPUMillis != old.CPUMillis || r.MemoryBytes != old.MemoryBytes {
		res := container.Resources{
			NanoCPUs: int64(r.CPUMillis) * 1_000_000,
			Memory:   int64(r.MemoryBytes), // #nosec G115 -- sizes far below MaxInt64
		}
		if _, err := d.cli.ContainerUpdate(ctx, d.containerName(id), client.ContainerUpdateOptions{Resources: &res}); err != nil {
			return false, fmt.Errorf("update container: %w", err)
		}
	}
	return r.SharedMemoryBytes != old.SharedMemoryBytes, nil
}

// Ping reports whether the Docker engine answers (on a Mac, whether Docker
// Desktop is running), giving up at ctx's deadline. It starts nothing.
func (d *Driver) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("docker ping: %w", err)
	}
	return nil
}

// Close releases the driver's connection to the engine.
func (d *Driver) Close() error { return d.cli.Close() }

// Capabilities describes the engine.
func (d *Driver) Capabilities(ctx context.Context) (driver.Capabilities, error) {
	v, err := d.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return driver.Capabilities{}, fmt.Errorf("docker version: %w", err)
	}
	c := driver.Capabilities{Driver: "docker", EngineVersion: v.Version, Isolation: driver.IsolationContainer}
	switch v.Arch {
	case "arm64", "aarch64":
		c.Architectures = []driver.Architecture{driver.ArchitectureARM64}
	case "amd64", "x86_64":
		c.Architectures = []driver.Architecture{driver.ArchitectureAMD64}
	}
	return c, nil
}

func (d *Driver) running(ctx context.Context, id driver.BoxID) (bool, error) {
	cctx, cancel := call(ctx, 0)
	defer cancel()
	insp, err := d.cli.ContainerInspect(cctx, d.containerName(id), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, engineErr(ctx, "inspect container", err)
	}
	return insp.Container.State != nil && insp.Container.State.Running, nil
}

// Known reports whether the driver has a spec for the box (Create was
// called and Destroy was not).
func (d *Driver) Known(id driver.BoxID) bool {
	p, err := d.specPath(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// ImageDigest returns ref pinned to the registry digest it resolved to
// ("repo:tag@sha256:..."), or ref unchanged when it is already pinned or the
// image is local only (built here, never pushed).
func (d *Driver) ImageDigest(ctx context.Context, ref string) (string, error) {
	if strings.Contains(ref, "@sha256:") {
		return ref, nil
	}
	cctx, cancel := call(ctx, 0)
	defer cancel()
	insp, err := d.cli.ImageInspect(cctx, ref)
	if err != nil {
		return "", engineErr(ctx, "inspect image "+ref, err)
	}
	repo := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo = ref[:i]
	}
	for _, rd := range insp.RepoDigests {
		name, digest, ok := strings.Cut(rd, "@")
		if ok && (name == repo || strings.HasSuffix(name, "/"+repo)) {
			return ref + "@" + digest, nil
		}
	}
	return ref, nil
}

// Rekey implements driver.Driver (ADR 0014): new secrets for the running
// box, written on its stdin through a fresh attach (never exec), as at
// start. The engine holds the stdin pipe, so this works after the process
// that started the box has gone. It returns once the agent answers with
// the new secrets.
func (d *Driver) Rekey(ctx context.Context, id driver.BoxID) error {
	if running, err := d.running(ctx, id); err != nil {
		return err
	} else if !running {
		return fmt.Errorf("workspace %s is not running", id)
	}
	// Before any key material exists: a box started with other mounts (the
	// whole storage folder, from before R-0201) must not get a new channel
	// while it can still reach what it shouldn't.
	s, err := d.loadSpec(id)
	if err != nil {
		return err
	}
	insp, err := d.cli.ContainerInspect(ctx, d.containerName(id), client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect the workspace's mounts: %w", err)
	}
	if insp.Container.HostConfig == nil || !sameMounts(d.hostConfig(s).Mounts, insp.Container.HostConfig.Mounts) {
		return driver.ErrMountsChanged
	}
	sec, err := agent.NewChannelSecrets()
	if err != nil {
		return err
	}
	line, err := sec.Line()
	if err != nil {
		return err
	}
	att, err := d.cli.ContainerAttach(ctx, d.containerName(id), client.ContainerAttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return fmt.Errorf("attach to re-key the agent channel: %w", err)
	}
	_, err = att.Conn.Write(line)
	att.Close()
	if err != nil {
		return fmt.Errorf("re-key the agent channel: %w", err)
	}
	d.mu.Lock()
	if d.channels == nil {
		d.channels = map[driver.BoxID]agent.ChannelSecrets{}
	}
	d.channels[id] = sec
	d.mu.Unlock()
	// The agent applies the line on its own time: wait until it answers.
	ch, err := d.AgentChannel(ctx, id)
	if err != nil {
		return err
	}
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c := agentv1.NewAgentServiceClient(conn)
	deadline := time.Now().Add(10 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := c.GetVersion(cctx, &agentv1.GetVersionRequest{})
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			d.mu.Lock()
			delete(d.channels, id)
			d.mu.Unlock()
			return fmt.Errorf("the workspace helper didn't take the new channel: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// agentPort is the agent channel inside the box.
var agentPort = network.MustParsePort(strconv.Itoa(agent.ChannelPort) + "/tcp")

// AgentChannel implements driver.Driver: the agent port as published on
// 127.0.0.1, with this start's certificate and token.
func (d *Driver) AgentChannel(ctx context.Context, id driver.BoxID) (driver.AgentChannel, error) {
	d.mu.Lock()
	sec, ok := d.channels[id]
	d.mu.Unlock()
	if !ok {
		return driver.AgentChannel{}, driver.ErrNoChannel
	}
	cctx, cancel := call(ctx, 0)
	defer cancel()
	res, err := d.cli.ContainerInspect(cctx, d.containerName(id), client.ContainerInspectOptions{})
	if err != nil {
		return driver.AgentChannel{}, engineErr(ctx, "inspect container", err)
	}
	var addr string
	if ns := res.Container.NetworkSettings; ns != nil {
		for _, b := range ns.Ports[agentPort] {
			if b.HostIP.IsLoopback() && b.HostPort != "" {
				addr = net.JoinHostPort(b.HostIP.String(), b.HostPort)
			}
		}
	}
	if addr == "" {
		return driver.AgentChannel{}, fmt.Errorf("workspace %s: the agent port is not published on the loopback address", id)
	}
	return driver.AgentChannel{
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		},
		Addr:    addr,
		CertPEM: sec.CertPEM,
		Token:   sec.Token,
	}, nil
}
