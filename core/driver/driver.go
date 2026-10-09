// SPDX-License-Identifier: Apache-2.0

// Package driver defines the box driver interface, the only boundary through
// which Portenv talks to a container or VM engine (Docker, Apple
// Containerization, microVMs). Only packages under core/driver may import an
// engine SDK or run an engine CLI; internal/boundary enforces this.
//
// The interface mirrors portenv.driver.v1.DriverService, which drivers in
// other languages (the Swift shim for Apple Containerization) serve over a
// local socket. Keep the two in step; TestInterfaceMatchesProto checks the
// method set.
//
// Terminals, ports and files never go through a driver: they go through the
// box agent. Exec is for bootstrap only.
package driver

import (
	"context"
	"errors"
	"iter"
	"net"
	"time"
)

// BoxID identifies a box. It is stable across machines and renames.
type BoxID string

// State is the lifecycle state of a box on one machine.
type State int

// Box states, mirroring portenv.types.v1.BoxState.
const (
	StateUnspecified State = iota
	StateCreated
	StateStarting
	StateRunning
	StateStopping
	StateStopped
	StateFailed
)

// Architecture is a CPU architecture, mirroring portenv.types.v1.Architecture.
type Architecture int

// Supported architectures.
const (
	ArchitectureUnspecified Architecture = iota
	ArchitectureARM64
	ArchitectureAMD64
)

// Isolation says how strongly an engine isolates boxes, mirroring
// portenv.driver.v1.Isolation.
type Isolation int

// Isolation levels.
const (
	IsolationUnspecified Isolation = iota
	// IsolationContainer shares a kernel with other boxes (Docker).
	IsolationContainer
	// IsolationVM runs each box in its own virtual machine.
	IsolationVM
)

// LogStream names the console stream a log chunk came from.
type LogStream int

// Console streams.
const (
	LogStreamUnspecified LogStream = iota
	LogStreamStdout
	LogStreamStderr
)

// Box is what a driver needs to create a box's instance.
type Box struct {
	ID   BoxID
	Name string
	// ToolboxImage is an OCI image reference, e.g. "portenv/toolbox-node:v12".
	ToolboxImage string
	Resources    Resources
}

// Resources are a box's start-time settings. Zero values mean no limit, or
// the default where one exists.
type Resources struct {
	CPUMillis         uint32
	MemoryBytes       uint64
	SharedMemoryBytes uint64 // 0 means the default (1 GiB)
	DockerInBox       bool
	GPU               bool
}

// ExecRequest is one non-interactive bootstrap command.
type ExecRequest struct {
	Argv    []string // not run through a shell
	Env     []string // "KEY=value"
	User    string   // empty means root
	Stdin   []byte
	Timeout time.Duration // 0 means no timeout
}

// ExecResult is the outcome of an ExecRequest.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// LogOptions select console output.
type LogOptions struct {
	// Follow keeps the stream open and yields new output as it arrives,
	// until ctx is cancelled.
	Follow    bool
	Since     time.Time // zero means from the beginning
	TailLines int       // 0 means all
}

// LogChunk is a piece of console output.
type LogChunk struct {
	Stream LogStream
	Data   []byte
	Time   time.Time
}

// Stats is a point-in-time snapshot of a box.
type Stats struct {
	State            State
	Time             time.Time
	CPUTime          time.Duration
	MemoryBytes      uint64
	MemoryLimitBytes uint64 // 0 means no limit
	ProcessCount     int
}

// HomeStorage refers to a box's encrypted home storage in the driver's own
// terms: a named volume on an encrypted disk for Docker, an ext4 disk image
// for Apple Containerization (ADR 0005).
type HomeStorage struct {
	Ref string
	// Fresh marks new, empty storage: the box creates its home from the
	// image's skeleton (resume rule 1). Never set for existing storage.
	Fresh bool
}

// Capabilities describe a driver and its engine.
type Capabilities struct {
	Driver        string // "docker", "apple" or "microvm"
	EngineVersion string
	Isolation     Isolation
	Architectures []Architecture
	GPU           bool
	DockerInBox   bool
}

// Driver runs boxes on one engine.
type Driver interface {
	// Create creates the box's instance from its toolbox image without
	// starting it.
	Create(ctx context.Context, box Box) (State, error)
	// Start starts a created or stopped box.
	Start(ctx context.Context, id BoxID) (State, error)
	// Stop stops a running box, forcing it after timeout (0 means the
	// driver's default).
	Stop(ctx context.Context, id BoxID, timeout time.Duration) (State, error)
	// Destroy removes the box's instance. It never deletes home storage.
	Destroy(ctx context.Context, id BoxID) error
	// Exec runs one non-interactive command in the box. Bootstrap only;
	// never an access path for people or agents.
	Exec(ctx context.Context, id BoxID, req ExecRequest) (ExecResult, error)
	// Logs yields the box's console output. A non-nil error ends the
	// sequence.
	Logs(ctx context.Context, id BoxID, opts LogOptions) iter.Seq2[LogChunk, error]
	// Stats returns a snapshot of the box's state and resource use.
	Stats(ctx context.Context, id BoxID) (Stats, error)
	// SetResources applies new start-time settings. It reports whether they
	// take effect only after a restart; the caller saves and restarts.
	SetResources(ctx context.Context, id BoxID, r Resources) (restartRequired bool, err error)
	// MountHome attaches the box's encrypted home storage at /home. It is
	// called after Create and before Start, while the box is stopped.
	MountHome(ctx context.Context, id BoxID, home HomeStorage) error
	// Capabilities describes the driver and its engine.
	Capabilities(ctx context.Context) (Capabilities, error)
	// AgentChannel returns the way to the box agent's API for the current
	// start (ADR 0010). It fails with ErrNoChannel when this driver did not
	// start the box (the secrets exist only in the process that started it);
	// stopping and starting the box again opens a new channel.
	AgentChannel(ctx context.Context, id BoxID) (AgentChannel, error)
	// Rekey gives this process a new channel to the running box without
	// restarting it (ADR 0014): fresh secrets reach the box agent the way
	// the first ones did, and the old ones stop working. It returns once the
	// agent answers on the new channel. A box that isn't running is an
	// error; nothing is started.
	Rekey(ctx context.Context, id BoxID) error
}

// AgentChannel reaches the box agent: Dial opens a connection, CertPEM is
// the certificate the agent must present, Token goes with every call.
type AgentChannel struct {
	Dial    func(ctx context.Context) (net.Conn, error)
	Addr    string // where Dial connects, 127.0.0.1:PORT on this machine
	CertPEM []byte
	Token   string
}

// ErrNoChannel: this process has no agent channel for the box's start.
var ErrNoChannel = errors.New("no agent channel for this start of the box (restart it to open one)")
