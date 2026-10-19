# 0023. A workspace keeps running when its window closes

Status: accepted (owner, CC-0349; scoped by the reviewer, R-0238). Replaces the owner's 2026-10-11 rule that closing the last window saves, releases and stops the workspace (PLAN 1.5).

## Context

R-0229's rerun of #119: a long-running command and a second tab were gone after the window was closed and reopened. That was by design: closing the last window did what quitting does. Agents working while the window is closed (ADR 0022) need the opposite.

## Decision

1. **Closing the window leaves the workspace running.** The app asks portenvd nothing: no save, no release, no stop. The workspace keeps its lease, its tmux sessions and its programs, and autosave carries on as for any open workspace. The window shows "Your workspaces".
2. **The list shows it as "Running"**, with Open (back to its window: OpenBox finds it already open, so no resume rule runs and nothing is restored) and **Close Workspace**.
3. **What closes it** (one release save, then the lease is released and it stops):
   - **Close Workspace** in the list. A failed save asks, as before: Try Again, Close Anyway (the quit marker; saved first thing on the next open) or Cancel.
   - **Quit:** a confirmation names the running workspaces ("acme-api and test-119 are running. Quitting saves and closes them."), then each is closed in turn, with the same three choices on a failed save. Cancel at any point stops the quit, and the rest keep running.
   - **Move To**, unchanged.
   - **No idle timeout** in the tester build.
   - **Logout, shutdown or a crash** are unchanged: portenvd's shutdown leaves each open workspace with the quit marker, and the next open saves first thing.
4. **CloseBox is logged by workspace ID** as OpenBox is (#134): `close` on entry, `close saved` with the snapshot, `close released`, and `close failed` with the reason, paths hidden.

## Invariants

None change (PLAN, Save, resume and leases). The window stops being a save trigger: the release save moves to Close Workspace and Quit. A workspace left running keeps the lease, so another machine opening it gets the lease sheet as before.

## Tests (written first)

- `KeepRunningTests`:
  - closing the last window calls nothing;
  - only workspaces open on this Mac count as running;
  - Quit closes each running workspace once, in order, and nothing when none run;
  - Cancel on a failed save stops the quit with the rest still running;
  - Quit Anyway leaves the marker and goes on.

  Each rule is mutation-checked.
- `TestCloseBoxIsLogged`: the log lines, with no paths.
- Still to extend, in a box: `tests/e2e/daemon.sh`, for a window close with no save and a reopen with the same tmux session.
