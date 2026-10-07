# 0005. restic runs inside the box, as a dedicated non-dumpable user

Date: 2026-10-07 · Status: accepted

## Context

A box's home lives inside the box: a Docker named volume, or an ext4 disk image mounted by the box's VM on Apple Containerization. The sync engine (ADR 0004) has to run restic somewhere that can read and restore that home. The owner set three constraints:

1. The repository password is never readable by agent lanes, on disk or in a process they can inspect.
2. Snapshots are consistent while the box runs, without a second VM or process reading a disk image the box has mounted read-write.
3. One mechanism works for both the docker and apple drivers.

The owner's preferred direction: restic runs inside the box as a root-owned process started by `portenv-agent`, with the password passed in over the agent channel for each run and never written to disk.

## Options considered

- **restic on the host reading the home.** Impossible for Apple (the host cannot safely read an ext4 image the VM has mounted read-write: constraint 2) and different per driver (constraint 3). Rejected.
- **A sidecar container sharing the volume.** Works for Docker only (constraint 3). Rejected.
- **The agent streams the home to restic on the host** (for example as a tar). No incremental parent detection, so every save re-reads and re-sends everything; misses the save budget. Rejected.
- **restic inside the box, started by the agent** (the preferred direction). Satisfies constraints 2 and 3. Tested against constraint 1 below.

## Test against constraint 1

`tests/e2e/restic-isolation.sh` starts a slow restic backup inside a toolbox box and, while it runs, tries every way to reach the password: the process environment, the password pipe (`/proc/<pid>/fd/3`), the command line, a full scan of the process's memory, and ptrace attach. It does so as root in the box, as root after switching to restic's own uid, and as a lane-like unprivileged user (uid 2000). It then searches the whole file system for the password.

| restic runs as | root in the box | root switched to restic's uid | lane user | on disk |
| --- | --- | --- | --- | --- |
| plain root (the preferred direction as written) | **reads it**: environment, pipe, memory (password found) and ptrace | denied | denied | not found |
| `portenv-sync` with file capabilities (this decision) | denied (all five probes) | denied | denied | not found |

A root process is readable by every other root process: Linux allows `/proc/<pid>/mem`, `environ`, `fd` and ptrace between processes of the same uid without any capability. Root in the box is not hypothetical: the user `work` has passwordless sudo, a stand-in agent acts as `work`, and an approved `sudo` (Phase 4) makes an agent root for a command. The preferred direction therefore fails constraint 1 as written, and passes with the change below.

## Decision

- **restic runs inside the box, started by `portenv-agent` for each run**, never by anything else.
- **It runs as `portenv-sync`** (uid 990), a dedicated system user, from `/usr/local/libexec/portenv/restic` (owner `root:portenv-sync`, mode 0750) carrying the file capabilities `cap_chown,cap_dac_override,cap_fowner,cap_fsetid`, enough to read every home and restore ownership. Gaining capabilities at exec makes the process **non-dumpable**, and reading a non-dumpable process (memory, environment, file descriptors, ptrace) needs `CAP_SYS_PTRACE`. Every capability must be in Docker's default set, otherwise exec fails with EPERM (`CAP_DAC_READ_SEARCH` is not, and `CAP_DAC_OVERRIDE` covers it).
- **No process that runs user or agent code has `CAP_SYS_PTRACE`**, nor `CAP_SYS_MODULE`, `CAP_SYS_RAWIO` or `CAP_BPF`. Docker's default set already excludes them; on Apple the agent removes them from the bounding set of every terminal and lane it starts (milestone 1.3), because there the box's root is the VM kernel's root.
- **The password travels only through pipes**: from the key store to `portenvd`, over the agent channel to `portenv-agent restic` on stdin (JSON, with any storage credentials), then on an inherited pipe to restic (`RESTIC_PASSWORD_FILE=/dev/fd/3`). It is never in argv, never in the environment of a readable process, never on disk. `portenv-agent restic` makes itself non-dumpable before reading it.
- **The agent runs only the restic it was built with**: the image build embeds restic's SHA-256 in the agent, and the agent hashes the binary and executes that same open file (no window to swap it). Root in the box can replace files, but a replaced restic is refused.
- **The root file system is fresh at every start** (the docker driver recreates the container from its spec; only `/home` persists), so tampering with image files never outlives a session.
- **Allow-listed runs**: the agent runs only `backup`, `cat`, `check`, `forget`, `init`, `prune`, `restore`, `snapshots`, `tag`, `unlock` and `version`, refuses flags that replace the password source or skip encryption, and accepts only storage-credential environment variables.
- **Phase 0 transport**: the agent channel is the driver's `Exec` with the JSON on stdin (`docker exec` is allowed in Phase 0). Phase 1 moves the same call onto the agent's own channel; nothing else changes.
- **`MountHome`** attaches the home storage before Start: a named volume on Docker (on an encrypted disk: FileVault on a Mac, LUKS on servers in milestone 0.5), an ext4 disk image on Apple. `HomeStorage.Fresh` tells a brand-new box to create its home from the skeleton (ADR 0003).

## Conditions

The guarantee that root in the box cannot read the repository password holds only while all of these are true. Each has a test.

| Condition | Why | Enforced by |
| --- | --- | --- |
| No box starts with `CAP_SYS_PTRACE`, `CAP_SYS_ADMIN`, `CAP_SYS_MODULE`, `CAP_SYS_RAWIO`, `CAP_PERFMON` or `CAP_BPF` in its bounding set | Any of them lets root read a non-dumpable process's memory or reach the kernel | `portenv-agent` checks its bounding set as the first start step and reports FAILED, naming the capability; unit test `TestCheckIsolation`; `TestAgentRefusesForbiddenCapabilities` starts real boxes with `CAP_SYS_PTRACE`, `CAP_SYS_ADMIN` and `--privileged` and expects refusal |
| A box is never privileged | A privileged container has every capability and every device | The same agent check (privileged means all forbidden capabilities); `TestHostConfigKeepsIsolation` asserts the docker driver never sets it |
| No added capabilities, no host PID, IPC, network or user namespace, no host devices, no `unconfined` seccomp or AppArmor | Each widens what root in the box can reach | `TestHostConfigKeepsIsolation` |
| restic runs only as `portenv-sync` from the hashed binary, launched by the agent | Non-dumpable process, no trojan restic | `tests/e2e/restic-isolation.sh`; the agent's hash check |
| In a VM, every process the agent starts for users and agents runs with these capabilities dropped from its bounding set | A VM's root holds every capability by default | Phase 1, milestone 1.3: the agent drops them itself, and the isolation probe must pass inside the VM |

"Docker inside the box" (a start-time setting, off by default) usually needs `CAP_SYS_ADMIN` or a privileged box. It must either be built without granting any of these capabilities to the box, or warn plainly that turning it on weakens this protection. That design is a Phase 1 question (docs/PLAN.md, Open questions).

## Constraint 2: what "consistent" means here

restic reads the home through the box's own kernel and file system, so a snapshot never sees a torn file system: there is no second reader of a mounted disk image. It is file-consistent but not point-in-time across files: a file being written during a save may be captured mid-write, and two files written together may be captured at different moments.

- **Release saves become point-in-time in Phase 1**, once the agent owns the terminals: closing and Move To stop the box's terminals and lanes first, then save. The Phase 0 CLI saves, then stops the box.
- **Autosaves and save points are live**: databases in the box use the `pre-save` hook (already in the plan) to write a consistent dump first.
- Freezing every process for each autosave would make saves point-in-time but pause the terminal for seconds every five minutes; not adopted. A file system with snapshots (for example btrfs on the Apple disk image) could give live point-in-time saves later; recorded as an open question.

## Consequences

- The toolbox image adds restic (built from source at the pinned version), `libcap2-bin`, the `portenv-sync` user and `/var/cache/portenv-sync` (restic's cache: encrypted data only, outside `/home`).
- The sync engine gains an `Executor` seam: `LocalExecutor` for tests, `AgentExecutor` for boxes (restic and path checks run in the box).
- `tests/e2e/restic-isolation.sh` is the regression test for constraint 1 and runs in CI after the image build.
- Constraint 1 depends on no workload process holding `CAP_SYS_PTRACE`. Adding it (for debugging tools inside a box, for example) would reopen this decision.
