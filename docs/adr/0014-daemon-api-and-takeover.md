# 0014. 1.1: the app on portenvd's API, a login item, and taking over running boxes

Date: 2026-10-09 · Status: accepted (the owner chose re-key over stdin and gRPC Swift 2, with the conditions below)

## Context

Milestone 1.1 (PLAN.md, Phase 1 item 3) has four parts:

- The app talks to `portenvd` over gRPC on its socket instead of running the `portenv` helper for each action, with push updates for the state line instead of the 3 s poll.
- A login item starts `portenvd`.
- The roughly 1.6 s wait before the terminal is usable goes.
- The Phase 0 CLI's `docker exec` path is removed.

The owner added a requirement: **a new `portenvd` takes over running boxes instead of restarting them.** Today a restart (crash, update, login item relaunch) restarts each open box, because the agent channel's secrets lived only in the old daemon's memory (ADR 0010). Programs in the box's terminal end. From 1.8, Sparkle restarts `portenvd` at every update, and every update would kill running dev servers and agents' long tasks.

Two of these needed a decision: how the new daemon gets a channel to a running box, and which Swift gRPC library the app uses. The owner chose re-key over stdin and gRPC Swift 2.

## Taking over a running box

The new daemon has no secret for the running agent. Any way in must be no easier for an attacker than starting the box was.

- **The app hands the old secrets to the new daemon.** Rejected. A Sparkle update relaunches the app as well as `portenvd`, so the app's memory is gone too, in exactly the case this is for. It also puts channel secrets in a second process.
- **`portenvd` keeps the channel secrets in the Keychain** (on servers, a root-only file) and reads them back after a restart. It works without touching the box. But it puts secrets at rest that ADR 0010 keeps only in memory. And it depends on the daemon reading its own Keychain items without a prompt, which holds only once the build is signed with a stable identity (ADR 0013). Rejected.
- **Re-key over the box's stdin (chosen).** The box keeps the stdin pipe that delivered the first secrets open:
  - **The container:** `OpenStdin` is set with `StdinOnce` off.
  - **The agent's API process** keeps reading it. Each new line holds a new token and a new certificate and key, and the agent swaps them in place (the TLS certificate through `GetCertificate`, the token atomically) and drops connections made with the old ones.
  - **The new daemon** generates fresh secrets and attaches to the running container's stdin (attach, never exec). It writes them as one line and dials the channel with them.
  - **The box keeps running,** and so do its tmux sessions and everything in them.
  - **Who can do this:** only someone who can attach to the container. That's the same Docker API access that started the box and handed over the first secrets, so no new power.
  - **On servers,** the runner re-keys the same way after its own restart.
  - **The Apple driver (1.3)** gets the same `Rekey` through its own control path.
  - **Nothing is stored anywhere,** and it works for dev builds today.

### This supersedes one condition of ADR 0010

ADR 0010 says the API process "reads the secrets and closes stdin". This ADR replaces that condition: the API process keeps stdin open and accepts later lines, and each line replaces the secrets.

**Why:** the only other ways for a new daemon to reach a running agent either put the secrets at rest (the Keychain) or depend on a process that a Sparkle update also restarts (the app). The stdin pipe is held by the engine and outlives `portenvd`. Using it again gives no new power: whoever can attach to the container started it.

ADR 0010's other conditions stay, with their tests. Init still hands stdin to the API process without reading it, and points its own fd 0 at `/dev/null`.

### Conditions

| Condition | Why | Enforced by |
| --- | --- | --- |
| **The engine holds the pipe's write end, never `portenvd`.** Docker: `OpenStdin` on, `StdinOnce` off. The shim keeps the container's stdin open across attaches, so a line written after the daemon was killed with `kill -9` still arrives. | Otherwise the pipe closes with the daemon, and take-over silently fails. | Probed on Docker Desktop on 2026-10-09: three attaches, the second killed with `kill -9` mid-attach; all three lines arrived and stdin stayed open. A driver conformance test does the same with the box agent. The server's Docker Engine is confirmed at the next server session, through the runner. |
| **The Apple driver (1.3) holds it outside `portenvd` too.** A Virtualization.framework VM lives in the process that runs it. So the shim runs each box's VM in its own helper, started by launchd and never a child of `portenvd`. The helper holds the VM and the agent's stdin (its console), and serves a re-key call on a socket only the user can reach (0600 in a user-only directory, caller's uid checked). | If the VM's lifetime or its stdin depended on `portenvd`, take-over would silently not work there. | The same conformance test, which every driver must pass before it ships. |
| **Root in the box can't write a re-key line.** The API process stays non-dumpable, and no process in the box has `CAP_SYS_PTRACE` (ADR 0005), so root can't open `/proc/<pid>/fd/0` or attach to the process. | Only the real daemon may re-key. | `TestChannelConditions` gains a probe: root in the box tries to write to the API process's stdin through `/proc` and fails. A control shows the same probe succeeds on an ordinary process. |
| **One `portenvd` at a time,** per Portenv directory: an exclusive lock taken at start. A second daemon exits with a plain error, and never re-keys a box the first one still serves. | Two daemons would re-key each other's boxes in turn. | `TestOneDaemonAtATime` (the lock). An e2e test starts a second daemon while the first serves a box, and checks it exits and the first keeps the box's channel. |
| **The agent reads re-key lines only from stdin,** in the existing strict format (one JSON line, known fields only), at most 16 KiB. A longer or malformed line is dropped whole, and the old secrets keep working. | A broken or hostile line mustn't leave the box without a channel. | Unit tests: malformed lines, an oversized line and unknown fields are ignored, and the old token still works. |
| **The swap is atomic.** The new token and certificate take effect together. Connections made with the old secrets are closed, and calls with the old token are refused. The line is never logged or echoed. | Old secrets must stop working at once. | Unit tests: after a re-key, the old token is refused and an old connection is closed; the new one works. The log holds no part of the line. |

### Acceptance

- **On the Mac:** `kill -9 portenvd` while a long command runs in the terminal. Afterwards the command is still running, the terminal reconnects, the state line recovers, and the box hasn't restarted (same container start time).
- **On a server, through the runner,** at the next server session: the same test.
- **A Sparkle-style relaunch** (quit and reopen the app and `portenvd` together) keeps the box and its programs running. An update quits differently from ⌘Q: the app tells `portenvd` it's relaunching, and `portenvd` exits without closing its boxes (as the runner already does). The new app and daemon then take over. ⌘Q still closes and saves.

### When the re-key fails

If the attach or the dial fails, or the box isn't running, the current behaviour stays (PLAN.md item 1g): the line says "Not saved since …", and the box is reopened once (restarted).

## The app's gRPC client

- **gRPC Swift 2 (chosen).**
  - **The packages:** `grpc-swift-2` with `grpc-swift-nio-transport`, whose HTTP/2 transport supports Unix domain sockets. Messages come from `grpc-swift-protobuf` and `swift-protobuf`.
  - **Maintained** by the gRPC project with Apple's Swift team, Apache-2.0. Apple's own `container` tool talks to its API server this way.
  - **Generated code is committed:** Swift code generated from `proto/` goes in `proto/gen/swift`, its own Swift package, the same as `proto/gen/go`. The plugins are pinned in the Makefile. `make proto` regenerates both, and `make proto-check` covers both.
  - **What it removes:**
    - running the `portenv` helper for every action;
    - the parsing of helper output;
    - the "main-actor work doesn't run during the quit wait" workaround.
- **Keep the CLI helper and add one streaming command** (rejected) (`portenv app watch`). No new dependency, but the helper stays, with its per-call process and text parsing. The plan says to replace it. Not recommended.

### Conditions for the client

- **Minimum macOS:** gRPC Swift 2 needs macOS 15 or later. The app targets macOS 26 (`apps/mac/Package.swift`, PLAN.md), so nothing is raised. The PR confirms it against the packages' manifests.
- **Only the user reaches the socket:** `portenvd`'s Unix socket is 0600, in a directory only the user can read. `portenvd` also checks every caller's uid (`LOCAL_PEERCRED`) and refuses anyone else. A test connects as another uid and is refused.
- **Pinned versions:**
  - the packages in `Package.resolved`;
  - the code generator plugins in the Makefile;
  - CI regenerates the Swift code from `proto/` and fails on any difference, as it does for Go.

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

- **launchd runs `portenvd`, never the app.**
  - `SMAppService.agent` registers `Contents/Library/LaunchAgents/com.portenv.portenvd.plist` (`BundleProgram` `Contents/Helpers/portenvd`, KeepAlive, RunAtLoad).
  - launchd starts `portenvd` at login and again whenever it exits, whether from a crash, `kill -9` or an update relaunch.
  - The app's own start and stop path is removed, and so is `PORTENVD_EXIT_WITH_PARENT`, so `portenvd` is never the app's child.
  - It never reads the Keychain (item 1g).
- **While `portenvd` is away,** the line shows "Not saved since …".
  - After a deadline (30 s), the window says plainly that Portenv's background service isn't running.
  - When Login Items needs the person's approval, it says so at once.
  - Both messages go when `portenvd` answers again.
- **⌘Q is unchanged** (the owner's decision, 2026-10-09). It closes, saves and releases the open box through the API, and leaves `portenvd` with launchd, with nothing open.
  - **Quit Anyway** calls `LeaveUnsaved`, because `portenvd` now outlives the app. It writes the quit marker and lets go of the box unsaved, as `portenvd`'s own shutdown used to. The next open takes the running box over and saves it first thing.
  - Only an update relaunch leaves boxes running.
- **The log:** run by launchd (`PORTENVD_LOG=file`), `portenvd` writes `~/Library/Logs/Portenv/portenvd.log`, 0600 in a 0700 folder, since a plist inside the bundle can't name the user's home.
- **Dev and test runs** with their own `PORTENV_HOME` start `portenvd` themselves, as the e2e scripts do. The app only connects.
- **Tests:**
  - with the app closed, `kill -9 portenvd`, and launchd restarts it;
  - `portenv app relaunch`, and launchd starts the next `portenvd`;
  - `LeaveUnsaved`, unit-tested and in `daemon.sh`;
  - the deadline and the approval message, in the Swift tests.

## The Phase 0 CLI's `docker exec` path

Done in 1.1. The audit, command by command:

| Command | Touched a box? | Where it went |
| --- | --- | --- |
| `init` | No | Stays as a plain command |
| `join` | No | Stays as a plain command: the server-side enrolment Move To uses |
| `ssh-config` | No | Stays as a plain command |
| `status` | Yes: started the box and ran restic for the lease and the kept saves | Stays, reading local state only (the driver's stats and the recorded sync state). The lease and every save come from `portenv app history`. |
| `attach`, `app …` | Through `portenvd` | Stay |
| `resume` | Yes | `portenv app open` (`OpenBox`); `--take-over` is `OpenBoxRequest.take_over` |
| `save` | Yes | `portenv app save` (`SaveNow`, new) |
| `point`, `close` | Yes | `portenv app point` and `close` (existing RPCs) |
| `history` | Yes | `portenv app history` (`ListSaves`, new) |
| `housekeep` | Yes | `portenv app housekeep [--prune]` (`Housekeep`, new) |
| `move` | Yes, and `portenv resume` on the server | `portenv app move` (`MoveBox`: enrolment with `join`, then the runner) |

- **Removed with them:**
  - `Session.Sync` and `AgentExecutor` (restic through `docker exec`);
  - `WaitAgent`;
  - `ResumeOn` and `CloseOn` (the CLI on a server).
- **The driver's `Exec` is gone** from the `Driver` interface and from `DriverService`. The Docker driver's exec code now lives in a test file, so the shipped binaries can't exec into a box. The conformance suite inspects boxes through a `Probe` function that each driver's test supplies.
- **Tests moved over:**
  - `two-machines.sh` now drives two `portenvd`s and counts exec events (only its own probes);
  - `server-gate.sh` (Phase 0) is retired; `server-session.sh` is the server test.

## Order of work (small PRs)

1. **Push updates:** the `WatchBoxState` RPC and its tests (Go).
2. **The Swift client:** generated code, a client on the socket, the app moved off the CLI helper, the state line on the stream, the terminal attached on ready.
3. **The take-over** (done first, ahead of 1 and 2): the one-daemon lock, re-key in the agent and the docker driver, the daemon taking over on start, then the update relaunch. The acceptance tests are above.
4. **The login item.**
5. **Removing the CLI's `docker exec` path,** with the audit.

## Consequences

- A `portenvd` restart, including every Sparkle update, no longer ends programs in a box.
- The app depends on gRPC Swift 2 and SwiftNIO.
- ADR 0010's stdin condition changes as described above. Its other conditions are unchanged.
