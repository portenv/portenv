# 0014. 1.1: the app on portenvd's API, a login item, and taking over running boxes

Date: 2026-10-09 · Status: proposed

## Context

Milestone 1.1 (PLAN.md, Phase 1 item 3) has four parts:

- The app talks to `portenvd` over gRPC on its socket instead of running the `portenv` helper for each action, with push updates for the state line instead of the 3 s poll.
- A login item starts `portenvd`.
- The roughly 1.6 s wait before the terminal is usable goes.
- The Phase 0 CLI's `docker exec` path is removed.

The owner added a requirement: **a new `portenvd` takes over running boxes instead of restarting them.** Today a restart (crash, update, login item relaunch) restarts each open box, because the agent channel's secrets lived only in the old daemon's memory (ADR 0010). Programs in the box's terminal end. From 1.8, Sparkle restarts `portenvd` at every update, and every update would kill running dev servers and agents' long tasks.

Two of these need a decision: how the new daemon gets a channel to a running box, and which Swift gRPC library the app uses.

## Taking over a running box

The new daemon has no secret for the running agent. Any way in must be no easier for an attacker than starting the box was.

- **The app hands the old secrets to the new daemon.** Rejected. A Sparkle update relaunches the app as well as `portenvd`, so the app's memory is gone too, in exactly the case this is for. It also puts channel secrets in a second process.
- **`portenvd` keeps the channel secrets in the Keychain** (on servers, a root-only file) and reads them back after a restart. It works without touching the box. But it puts secrets at rest that ADR 0010 keeps only in memory. And it depends on the daemon reading its own Keychain items without a prompt, which holds only once the build is signed with a stable identity (ADR 0013). Not recommended.
- **Re-key over the box's stdin (recommended).** The box keeps the stdin pipe that delivered the first secrets open:
  - **The container:** `OpenStdin` is set with `StdinOnce` off.
  - **The agent's API process** keeps reading it. Each new line holds a new token and a new certificate and key, and the agent swaps them in place (the TLS certificate through `GetCertificate`, the token atomically) and drops connections made with the old ones.
  - **The new daemon** generates fresh secrets and attaches to the running container's stdin (attach, never exec). It writes them as one line and dials the channel with them.
  - **The box keeps running,** and so do its tmux sessions and everything in them.
  - **Who can do this:** only someone who can attach to the container. That's the same Docker API access that started the box and handed over the first secrets, so no new power.
  - **On servers,** the runner re-keys the same way after its own restart.
  - **The Apple driver (1.3)** gets the same `Rekey` through its own control path.
  - **Nothing is stored anywhere,** and it works for dev builds today.

### What changes in ADR 0010's conditions

- **Before:** the API process reads one line and closes stdin. **After:** it keeps stdin open and accepts later lines. Each line replaces the secrets, and the old ones are dropped from memory.
- **Unchanged:** init still hands stdin to the API process without reading it and points its own fd 0 at `/dev/null`. The pipe's other end is outside the box (Docker's attach).
- **Root in the box still can't inject a line or read one.** The API process is non-dumpable, and opening another process's `/proc/<pid>/fd/0` needs `CAP_SYS_PTRACE`, which no box has (ADR 0005).
- **New tests:**
  - **The take-over:** `kill -9 portenvd` while a long command runs in the terminal; the command keeps running and the state line recovers.
  - **Old secrets stop working:** a re-key ends the old token and certificate.
  - **Root can't inject:** `TestChannelConditions` gains a probe in which root in the box tries to write to the API process's stdin and fails, with a control showing the probe works on an ordinary process.

### When the re-key fails

If the attach or the dial fails, or the box isn't running, the current behaviour stays (PLAN.md item 1g): the line says "Not saved since …", and the box is reopened once (restarted).

## The app's gRPC client

- **gRPC Swift 2 (recommended).**
  - **The packages:** `grpc-swift-2` with `grpc-swift-nio-transport`, whose HTTP/2 transport supports Unix domain sockets. Messages come from `grpc-swift-protobuf` and `swift-protobuf`.
  - **Maintained** by the gRPC project with Apple's Swift team, Apache-2.0. Apple's own `container` tool talks to its API server this way.
  - **Generated code is committed:** Swift code generated from `proto/` goes in `proto/gen/swift`, its own Swift package, the same as `proto/gen/go`. The plugins are pinned in the Makefile. `make proto` regenerates both, and `make proto-check` covers both.
  - **What it removes:**
    - running the `portenv` helper for every action;
    - the parsing of helper output;
    - the "main-actor work doesn't run during the quit wait" workaround.
- **Keep the CLI helper and add one streaming command** (`portenv app watch`). No new dependency, but the helper stays, with its per-call process and text parsing. The plan says to replace it. Not recommended.

## Push updates

- **`WatchBoxState(name)`** streams `GetBoxStateResponse` whenever anything in it changes:
  - the save state;
  - the location;
  - the agent's readiness;
  - packages that failed to install;
  - an interrupted box.
- **No change, no message,** apart from a heartbeat every 30 s, so a dead stream is noticed.
- **The terminal attaches** the moment the stream reports the agent ready, which removes the 1.6 s poll.
- **When `portenvd` goes away,** the stream ends at once, which replaces the poll that noticed it (item 1g).

## The login item

- **How it starts:** `SMAppService.agent` registers a LaunchAgent plist in the app bundle (`Contents/Library/LaunchAgents`). launchd starts `portenvd` at login and restarts it if it exits.
  - The app stops starting `portenvd` itself, and `PORTENVD_EXIT_WITH_PARENT` goes.
  - It never reads the Keychain (item 1g).
- **Quitting the app** still closes and saves its open boxes (the quit alert flow), then leaves `portenvd` running with nothing open.
- **Dev builds run from `bin/`:** the app registers the agent from wherever the bundle is. The README says how to unregister it.

## The Phase 0 CLI's `docker exec` path

- **`init`** becomes a daemon call.
- **`resume`, `save`, `point`, `close`, `history`, `housekeep` and `move`** reach a box through the driver's `Exec`. Each one either moves into the daemon API or goes. The app already does all of them through `portenvd`.
- **`join`, `status` and `ssh-config`** stay as plain commands that never touch a box. On servers, `join` and `status` are the enrolment that Move To uses.
- **An audit** lists every command and where it went. Then the driver's `Exec` is removed if nothing uses it any more, and `tests/e2e/daemon.sh` checks the engine's exec events: none.

## Order of work (small PRs)

1. **Push updates:** the `WatchBoxState` RPC and its tests (Go).
2. **The Swift client:** generated code, a client on the socket, the app moved off the CLI helper, the state line on the stream, the terminal attached on ready.
3. **The take-over:** re-key in the agent and the docker driver, then the daemon taking over on start. The test is `kill -9` with a long command running.
4. **The login item.**
5. **Removing the CLI's `docker exec` path,** with the audit.

## Consequences

- A `portenvd` restart, including every Sparkle update, no longer ends programs in a box.
- The app depends on gRPC Swift 2 and SwiftNIO.
- ADR 0010's stdin condition changes as described above. Its other conditions are unchanged.
