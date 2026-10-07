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
	"io"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
)

const (
	labelBox   = "dev.portenv.box"
	namePrefix = "portenv-"
	// StorageMount is where Config.StorageDir appears inside every box.
	StorageMount = "/run/portenv/storage"
	defaultStop  = 10 * time.Second
	defaultShm   = 1 << 30
)

// Config configures the driver.
type Config struct {
	// StateDir holds each box's spec. Required.
	StateDir string
	// StorageDir, when set, is bind-mounted at StorageMount in every box so
	// restic inside the box can reach a local repository (Phase 0).
	StorageDir string
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

// Driver is the docker box driver.
type Driver struct {
	cfg Config
	cli *client.Client
}

var _ driver.Driver = (*Driver)(nil)

// New returns a driver connected to the Docker engine.
func New(cfg Config) (*Driver, error) {
	if cfg.StateDir == "" {
		return nil, errors.New("docker driver: StateDir is required")
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

// spec is what the driver remembers about a box between calls.
type spec struct {
	Box  driver.Box         `json:"box"`
	Home driver.HomeStorage `json:"home"`
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (d *Driver) specPath(id driver.BoxID) (string, error) {
	if !idRE.MatchString(string(id)) {
		return "", fmt.Errorf("invalid box ID %q", id)
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
		return spec{}, fmt.Errorf("box %s does not exist", id)
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
	if err := d.ensureImage(ctx, box.ToolboxImage); err != nil {
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

func (d *Driver) ensureImage(ctx context.Context, ref string) error {
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", ref, err)
	}
	resp, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer func() { _ = resp.Close() }()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// recreate replaces the box's container with a fresh one from its spec.
// The box must not be running.
func (d *Driver) recreate(ctx context.Context, s spec) error {
	name := d.containerName(s.Box.ID)
	if _, err := d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove old container: %w", err)
	}
	var env []string
	if s.Home.Fresh {
		env = append(env, "PORTENV_INIT_HOME=1")
	}
	host := d.hostConfig(s)
	_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:    s.Box.ToolboxImage,
			Hostname: "portenv",
			Env:      env,
			Labels:   map[string]string{labelBox: string(s.Box.ID)},
		},
		HostConfig: host,
	})
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// hostConfig is the container's host configuration. It never makes the box
// privileged, never adds capabilities, never shares the host's PID, IPC or
// network namespaces and never relaxes seccomp or AppArmor: root in the box
// must not be able to read another process's memory (ADR 0005, conditions).
// TestHostConfigKeepsIsolation enforces this.
func (d *Driver) hostConfig(s spec) *container.HostConfig {
	shm := int64(s.Box.Resources.SharedMemoryBytes) // #nosec G115 -- sizes far below MaxInt64
	if shm == 0 {
		shm = defaultShm
	}
	host := &container.HostConfig{
		ShmSize: shm,
		Resources: container.Resources{
			NanoCPUs: int64(s.Box.Resources.CPUMillis) * 1_000_000,
			Memory:   int64(s.Box.Resources.MemoryBytes), // #nosec G115 -- sizes far below MaxInt64
		},
	}
	if s.Home.Ref != "" {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: s.Home.Ref, Target: "/home"})
	}
	if d.cfg.StorageDir != "" {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: d.cfg.StorageDir, Target: StorageMount})
	}
	return host
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
		return errors.New("MountHome needs the box stopped")
	}
	if _, err := d.cli.VolumeInspect(ctx, home.Ref, client.VolumeInspectOptions{}); cerrdefs.IsNotFound(err) {
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
		if _, err := d.cli.VolumeCreate(ctx, opts); err != nil {
			return fmt.Errorf("create home volume: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect home volume: %w", err)
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
	if _, err := d.cli.ContainerStart(ctx, d.containerName(id), client.ContainerStartOptions{}); err != nil {
		return driver.StateFailed, fmt.Errorf("start container: %w", err)
	}
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
	if _, err := d.cli.ContainerStop(ctx, d.containerName(id), client.ContainerStopOptions{Timeout: &secs}); err != nil && !cerrdefs.IsNotFound(err) {
		return driver.StateUnspecified, fmt.Errorf("stop container: %w", err)
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

// Exec runs one non-interactive command in the box and collects its output.
func (d *Driver) Exec(ctx context.Context, id driver.BoxID, req driver.ExecRequest) (driver.ExecResult, error) {
	if len(req.Argv) == 0 {
		return driver.ExecResult{}, errors.New("exec: empty argv")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	return d.execNamed(ctx, d.containerName(id), req)
}

// execIn runs argv as root in a container by name (tests).
func (d *Driver) execIn(ctx context.Context, name string, argv []string) (driver.ExecResult, error) {
	return d.execNamed(ctx, name, driver.ExecRequest{Argv: argv})
}

func (d *Driver) execNamed(ctx context.Context, name string, req driver.ExecRequest) (driver.ExecResult, error) {
	user := req.User
	if user == "" {
		user = "root"
	}
	created, err := d.cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		User: user, Cmd: req.Argv, Env: req.Env,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return driver.ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	att, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return driver.ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()

	writeErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(att.Conn, bytes.NewReader(req.Stdin))
		if cerr := att.CloseWrite(); err == nil {
			err = cerr
		}
		writeErr <- err
	}()
	var stdout, stderr bytes.Buffer
	readDone := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			return driver.ExecResult{}, fmt.Errorf("exec output: %w", err)
		}
	case <-ctx.Done():
		return driver.ExecResult{}, ctx.Err()
	}
	if err := <-writeErr; err != nil {
		return driver.ExecResult{}, fmt.Errorf("exec stdin: %w", err)
	}
	insp, err := d.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return driver.ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	return driver.ExecResult{ExitCode: insp.ExitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
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
		return false, errors.New("docker driver: Docker in the box and GPU are not supported yet")
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
	insp, err := d.cli.ContainerInspect(ctx, d.containerName(id), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect container: %w", err)
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
