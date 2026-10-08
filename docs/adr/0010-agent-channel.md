# 0010. The agent channel: gRPC to the box agent over pinned TLS, loopback only, per-start token

Date: 2026-10-08 · Status: proposed

## Context

From Phase 1, `docker exec` is never an access path (CLAUDE.md). `portenvd` needs one channel to the box agent for everything it does in a box: readiness, restic runs and path checks (ADR 0005 already planned to move them off `docker exec`), and terminals attached to the box's tmux sessions. Only driver packages may talk to the engine, and the channel must work for the docker driver now and the apple driver (1.3) later.

The constraints: no port reachable from the network, nothing in a box other than root can use the channel, other users on the Mac cannot use it, and no secret on disk outside approved stores.

## Options considered

- **The container's stdio (docker attach) carrying a multiplexed stream.** No port at all, but a dropped attach leaves the agent mid-stream with a new peer, so both ends need a resynchronising framing layer, and console logs share the stream. Fragile for the one channel everything depends on. Rejected.
- **A unix socket in a bind-mounted host directory.** Docker Desktop cannot pass unix sockets between the Mac and its Linux VM. Rejected.
- **gRPC over TCP on the box's own network, published on the Mac's loopback address only, with a per-start token.** Works on every Docker engine; the apple driver can offer the same `Dial` over vsock. Chosen.

## Decision

- The agent serves `portenv.agent.v1.AgentService` (readiness, restic, path info, terminal) on TCP port 7700 inside the box. The docker driver publishes it on `127.0.0.1` with an ephemeral host port, never on another address.
- At every start the driver creates a 256-bit random token and a fresh TLS key and self-signed certificate for the agent, writes them into the new container before it starts (`/run/portenv/agent/`, root, 0600, on the fresh root file system, so they never outlive the start) with the engine's copy API, and keeps the token and certificate in memory only.
- Both ends authenticate. The client pins that exact certificate, so a process that takes over the loopback port after the box stops never receives the token or a password; every call then carries the token, which the agent compares in constant time.
- The API runs in `portenv-agent serve`, a child of the init process that makes itself non-dumpable before it reads the token, and that init restarts if it exits. Init itself never handles passwords, and restic and terminal processes are the API process's own children, so init's reaper never races their exit status.
- The driver interface gains `AgentChannel(ctx, id)`, which returns a dialer for the channel and the token. Callers never learn how it is carried; the apple driver will return a vsock dialer.
- The terminal call attaches a PTY to `tmux new-session -A -s <session>` as `work`, so closing and reopening the window lands in the same session.
- Phase 0's `portenv` CLI keeps `docker exec` (it is the Phase 0 tool, run on servers until the runner replaces it in 2.1). `portenvd` uses only the agent channel.

## Consequences

- Root in the box can read the token and key files, which only lets it talk to its own agent: no new power. `work` and agent lanes cannot. Reading the API process's memory (where passwords pass) needs `CAP_SYS_PTRACE`, which no box has (ADR 0005); `tests/e2e/restic-isolation.sh` probes that process too.
- Another user on the Mac can reach the loopback port but not the token, which lives only in `portenvd`'s memory.
- A restarted `portenvd` has lost the token, so it restarts a running box to open a new channel (the home is on its volume; nothing is lost).
- `TestHostConfigKeepsIsolation` gains a check that the only published port is the agent port, on 127.0.0.1.
