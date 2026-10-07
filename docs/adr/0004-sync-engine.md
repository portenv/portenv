# 0004. Sync engine: restic conventions, tag leases and the resume rules

Date: 2026-10-07 · Status: accepted

## Context

Milestone 0.3 builds `core/sync`, the data-loss path: saves, resumes and the tag-based lease used before the control plane exists. The plan fixes the rules and invariants; this records how they are implemented and the gaps found on the way.

## Decision

- **Identity of a home's content is the snapshot's tree hash**, not its ID. `restic tag` rewrites snapshots and changes their IDs; the tree does not change. Per-machine state (`<state>/state.json`, written atomically) holds the tree the local home was last synced to and a dirty flag (the box was opened since).
- **Change detection across machines uses an explicit `--parent`**: the snapshot with this machine's synced tree. restic has no option to store a fixed path, so the snapshot path is whatever the home's path is where restic runs (`/home` in production) and restores use `<snapshot>:<path>`. Where restic runs relative to the box (inside it, or in a helper with the home mounted at `/home`) is settled with `MountHome` in milestone 0.4.
- **Resume**, after listing snapshots and reading state:
  1. No snapshots: start; `ActionNewBox` only if the local home is empty (the agent then creates it with `PORTENV_INIT_HOME=1`). An existing local home is kept and the first save records it.
  2. Newest snapshot tagged `active:<other>`: `LeaseHeldError` unless the user takes over.
  3. Newest is not newer than the local home: start locally. "Newer" means made by another machine and not the synced tree. A snapshot this machine made itself is never newer than its own home, which also covers a save that finished after `portenvd` was killed but before the state was written.
  4. Newer snapshot and a dirty or unknown local home: save the local home tagged `orphaned`, then restore. Unknown state with a non-empty home counts as unsaved.
  5. Otherwise restore incrementally (`--overwrite if-changed --delete`). An empty home is always restored.
  Then the lease moves to this machine (`active:` tag on the newest snapshot) and the state is marked dirty before the box starts.
- **Invariants are enforced in code, not only by tests**: `Save` refuses an empty home; the restore path refuses a non-empty home that is dirty or of unknown state; a save is returned only after its snapshot is listed and the state records it; a save is refused with `LeaseChangedError` when the newest snapshot carries another machine's lease or another machine saved since this one synced, unless the user confirms (the other version stays in history).
- **Only the newest snapshot carries `active:`.** After each save, older active tags are removed in one batched call; failure there is tidying, not a failed save.
- **Stale leases (tag fallback):** a lease is stale when its snapshot is more than 10 minutes old. Autosaves happen only when there are changes, so an idle but open box also looks stale; the user decides, nothing is released automatically. The Phase 3 control plane replaces this with real heartbeats.
- **Password handling:** the repository key goes to restic through an inherited pipe (`RESTIC_PASSWORD_FILE=/dev/fd/3`), never on disk or in the environment; inherited `RESTIC_*` variables are dropped. restic's own integrity checks stay on.
- **restic is pinned** (0.19.1, installed into `bin/` by the Makefile like the other tools) and `Open` refuses anything older than 0.17. A lock left by a killed restic is removed with `restic unlock` (stale locks only) and the command retried once.
- **Retention:** `forget --group-by host` with the plan's policy and `--keep-tag orphaned`.

## Consequences

- Every restic call costs about half a second of key derivation (scrypt, calibrated at `init`). A save is four calls, a same-machine resume two. The sync tests run in parallel, one repository each.
- Tag leases are race-prone by nature (two machines opening at the same moment). The control plane in Phase 3 is authoritative; until then this is the documented fallback.
- Snapshot order is by time, so machines with badly skewed clocks can misjudge "newest". The server preflight (milestone 2.2) already checks clock sync.
- MinIO (S3) and SFTP repositories, and network performance budgets, belong to the e2e harness in `tests/e2e`.
