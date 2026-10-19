# 0029. Delete Workspace…

Date: 2026-10-19 · Status: proposed (built for the tester build, approved by the owner; reviewer additions R-0358)

## Context

The tester build had no way to delete a workspace: a tester who made a throwaway workspace kept it, its home, its saves and its key forever. PLAN.md's key flow 6 already set the rule: a box's keys go only together with its saves, and no box ever ends up with saves but no key on any device or in the recovery key (ADR 0019). Deleting is the one action in the app that destroys saves, so it needs a fixed order, resumable steps and a confirmation that names what goes.

## Decision

1. **Where it's offered:** Workspace › Delete Workspace… (the title "⌄" menu and the menu bar, last, after a divider) and the workspace list's context menu. Disabled while the window is busy or an agent of the workspace's team is running (R-0358).
2. **An open workspace is closed first, by the person.** The sheet of an open workspace says so and offers Close Workspace (the usual close: saved and released, with Try Again or Close Anyway on a failed save); the delete confirmation shows once it's closed. Chosen over closing it inside the deletion because the usual close keeps its own failure handling, and a deletion never stops running programs as a side effect. portenvd refuses a workspace open here, being saved, still saved in the background after Quit Anyway, moving, open on a server, running without this portenvd, or with an agent running ("Stop its agents first (stop its team), then delete it."), each with its reason.
3. **The confirmation sheet** names exactly what goes: all its saves on this Mac with their count and size (counted without a key, one file per save in the repository), everything in the workspace on this Mac, and its key in this Mac's Keychain. It says "This can't be undone." and that copies in Time Machine backups aren't deleted (the recovery key still opens them when the repository carries it). Delete Workspace (destructive) stays disabled until the workspace's name is typed exactly; Cancel is the default button.
4. **Saves elsewhere** (a server, a bucket) are never deleted by this version: Portenv has no safe way yet to delete a repository it reaches over SFTP or S3 (2.2's append-only credentials change who may). The sheet names the place and needs a second confirmation, "Leave its saves on <place>"; the workspace goes from this Mac and its saves and key stay, so it can be restored later (PLAN flow 6). portenvd refuses without that confirmation.
5. **portenvd's order** (`DeleteBox`), each step idempotent, the step reached recorded in the box's config (`deleting.step`) before the next starts:
   1. checks: the caller is the person at this Mac (never an agent), the typed name equals the name, the refusals of point 2, saves elsewhere confirmed, the instance not running, and no other machine holding the lease (read with the box's key; the app supplies it as for an open when the Keychain needs approval). Nothing is written before they pass;
   2. the instance (stop, remove the container and the driver's spec);
   3. the home storage (`DeleteHome`, a new driver call: refused while the instance exists, only volumes labelled with the box's ID, storage already gone is fine);
   4. the saves on this Mac: exactly the box's own repository folder;
   5. the key: portenvd answers with the key IDs to delete; the app deletes them from the Keychain (only the app touches the Keychain) and calls again with `keys_deleted`, which counts only once the saves are gone;
   6. the record, last: this Mac's state for the box, then its config, which takes the deletion's progress with it in one step. Keys held in memory and the team's in-memory state go too.
6. **A failure** says where it stopped ("Deleting acme-api stopped while deleting its saves. What's left of it is still there. Choose Delete Workspace… again to continue."). The list shows the workspace as not finished deleting; OpenBox refuses it; deleting again continues from the recorded step without the checks that need the key.
7. **The CLI:** `portenv app delete BOX` through portenvd with the same checks, with `--yes-delete-everything` (or the name typed at a terminal) and `--saves-elsewhere-stay` for saves elsewhere. It never touches the Keychain: when the key remains it says to finish in the app and fails. Like every owner's command, it's refused inside a workspace.
8. **Never an agent's:** no method of the workspace helper's API, no team call, no decision kind and no grant deletes a workspace, and portenvd's DeleteBox and GetDeletePlan refuse any caller but the person at this Mac. A test checks the agent surface.

## Invariants

- No workspace ends up with saves but no key: the key goes only after portenvd reports the saves gone; saves elsewhere keep their key.
- Nothing is half-gone silently: every step is recorded before the next, and the record goes last.
- A deletion never touches another workspace's instance, home, saves, state or key.

## What's left on purpose

- Saves on a server or in a bucket, and their key on this Mac (point 4).
- A server's own copy of the workspace (its config and key there) after a Move To and back: the server's runner keeps it until server-side deletion exists (Phase 2).
- Copies in Time Machine backups.

## Tests (written first)

- Go, `core/daemon/delete_test.go`: the order; resuming after a failure at each step; refusals under another machine's lease, a lease that can't be checked, open, saving, saving in the background, moving, on a server, running, an agent running; the exact name; the second confirmation for saves elsewhere; another workspace untouched; never an agent; the plan; OpenBox refusing a workspace being deleted; the agent surface.
- Go, `core/driver/docker/delete_home_test.go` against a fake engine, and the conformance suite's DeleteHomeAfterDestroy; `core/cmd/portenv/delete_test.go` for the CLI's flags.
- Swift, `WorkspaceDeletionTests`: the exact name, the sheet's words, the second confirmation, refusals, the menu, the key only after portenvd says the saves are gone, a Keychain refusal, the key read for portenvd, the list after deleting.

## Consequences

PLAN.md's key flow 6 and Phase 1 describe what's built; GUIDELINES §3.2 adds Delete Workspace… to the Workspace menu and a section on the delete sheet. The driver interface gains `DeleteHome` (driver.proto, additive).
