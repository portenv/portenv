# 0013. Only the app raises Keychain prompts; a stable signing identity ends them

Date: 2026-10-09 · Status: accepted

## Context

Closing `retest`, a new dev build's first Keychain read waited 14½ minutes behind a prompt while the window said only "Opening…" (PLAN.md, Phase 1 item 1).

**Root cause, confirmed (item 1e).** Box keys are generic passwords in the file-based login keychain. Its access lists trust a reader by its code signature. An ad-hoc signed build has no stable identity: the list records the build's cdhash, which changes on every rebuild. So "Always Allow" covers only the build it was given to, and the next build asks again. Confirmed on 2026-10-09: after "Toujours autoriser" (Always Allow), the same build read the key without asking. Re-signing that same binary under a new identifier (a new cdhash) brought the prompt back.

**What doesn't stop the prompt.** `kSecUseAuthenticationUI = Fail` alone doesn't stop the prompt for these items: in the real test it still showed one. Only `SecKeychainSetUserInteractionAllowed(false)` does. That call is deprecated, and it turns prompts off for the whole process, not for one call.

## Decision

- **Only the app prompts.** The app reads keys in Swift, off the main thread, in front of the person, and hands them to `portenvd` (`ProvideKeys`). `portenvd` holds them in memory only.
- **Every other process fails fast.** `portenvd`, the runner, the CLI without a terminal and the CLI as the app's helper (`portenv app …`) all fail at once with "Keychain needs your approval. Open Portenv on this Mac to allow it."
- **The process-wide switch stays on the daemon side.**
  - **Where it can run:** `SecKeychainSetUserInteractionAllowed(false)` is called only from `core/keys`, before a non-interactive call.
  - **One decision per process:** a process that may prompt (`keys.AllowPrompts`, the CLI at a terminal) never turns it off. Allowing prompts after it was turned off panics.
  - **The app never links it:**
    - no Swift code names it (`NoPromptSwitchTests`);
    - `make app` fails if the built executable imports it.
- **The fix is a stable signing identity.** Once the Apple Developer enrolment lands (1.8), the app and `portenvd` are signed with the Developer ID. Box keys then move to the data-protection keychain, with an access group shared by the app and `portenvd`. Access follows the team identity, so rebuilds no longer prompt. There, `kSecUseAuthenticationUI = Fail` is enough, and **the deprecated switch is removed** in the same change.
- **Until then,** test boxes on this Mac prompt once after each rebuild. The wait screen ("Waiting for Keychain access", Cancel) and the notification make that visible.

## Consequences

- No background process can leave a prompt waiting with no window to point at.
- On a dev Mac, a rebuild asks for each box's key once, in the app.
- Moving keys to the data-protection keychain is a key migration. Like any key code, it is written test-first: the old item is read, written to the new keychain, read back, and only then removed.
