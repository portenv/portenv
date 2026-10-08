# 0012. Every restic run has a deadline

Date: 2026-10-08 · Status: accepted

## Context

A mutation check in the test-path audit (#20) broke the REST forward: the first open then hung for 29 minutes in `portenvd`, and so did its shutdown save. restic retries an unreachable backend for many minutes, and nothing in Portenv bounded a run. A server that stops answering (a frozen process, a dropped link that still accepts connections) would hang a save, and the app would keep showing its last state.

## Decision

- **Every restic run has a deadline**, set where every run starts (`sync.restic.runWithin`):

  | Command | Deadline |
  | --- | --- |
  | `snapshots`, `tag`, `unlock`, `key`, `init`, `cat` and the rest | 60 s |
  | `check` (metadata only) | 5 min |
  | `backup`, `restore` | 2 min plus the data at 1 MiB/s (8 Mbit/s, well below the 20 Mbit/s budget) |

  The size comes from restic's snapshot summaries (`total_bytes_processed`): a restore uses its snapshot's size, and a backup uses the box's last save.
- **A run that misses its deadline is stopped cleanly:** SIGINT first, so restic releases its lock and cleans up, then SIGKILL after 15 s. On the Mac the executor does this; in the box the agent does it when the gRPC call's deadline passes.
- **Then it is retried once:** `unlock` (stale locks), `check` when the command writes (`backup`, `tag`, `forget`, `prune`, `init`, `key`), and the command again. A second miss is a `DeadlineError`, and the save fails. When the caller cancels (the app quits), nothing is retried.
- **The state line is honest:**
  - `Not saved since 14:58 · retrying` while the retry runs (`SAVE_STATE_RETRYING`);
  - `Not saved since 14:58` after a failed save, until one succeeds (`SAVE_STATE_NOT_SAVED`);
  - the time is always that of the last save that completed.
- **Tests:**
  - unit tests with a fake restic that hangs;
  - an e2e test that freezes the storage server's REST server (SIGSTOP): the save fails within its deadlines, the state says retrying and then not saved, and saves resume once the server answers;
  - `PORTENV_TEST_RESTIC_DEADLINES` shortens the deadlines for that e2e only.

## Consequences

- A save never hangs. A dead server costs at most three deadlines (the run, the unlock and the retry, plus the check for writes) before the user sees "Not saved since".
- A very slow but working link (under 1 MiB/s) can fail a large first backup; that link is far below the supported budget, and the failure says so instead of hanging.
- Autosave (1.4) retries failed saves on its own schedule; until then the next save point retries.
