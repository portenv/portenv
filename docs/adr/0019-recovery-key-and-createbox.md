# 0019. The recovery key, and making boxes from the app (CreateBox)

Date: 2026-10-11 · Status: proposed. Decisions 1 to 5 below are taken (the reviewer, on the owner's delegation, R-0087, 2026-10-11); the details this ADR works out wait for review, and code follows that review.

## Context

First run (1.5) has to make keys, a recovery key and the first box from the app. Until now only `portenv init` made boxes: the CLI makes a random repository key, puts it in the Keychain, writes `boxes/<name>.json`, and the repository is created by `restic init` on the box's first open. There was no recovery key at all.

Four open questions in `docs/PLAN.md` blocked this, because they're data-loss paths:

- the default protection mode;
- the recovery key's format, and how it becomes each box's extra restic key;
- how the app makes a box when only the app may raise Keychain prompts (ADR 0013) but `portenvd` owns box configs and storage;
- "Save to Passwords" on the recovery key step, for which macOS has no public API.

What the plan already says, and this ADR keeps:

- **Only you** means no escrow: losing every device and the recovery key loses the data, and setup says so (PLAN.md, Encryption and key management).
- **Delete a box:** no box ends up with saves but no key on any device or in the recovery key.
- **restic** allows several keys per repository. Every key's password goes through restic's own scrypt, calibrated where the key is added (about half a second, ADR 0004). Key commands (`init`, `key add`) run on the host, never in the box (ADR 0005).
- **Keychain:** only the app reads or writes it interactively; `portenvd` gets keys over `ProvideKeys` and holds them in memory only (ADR 0013).

## Decisions already made (R-0087)

1. **Default protection: Only you.** The recovery key is required at first run.
2. **The recovery key** is the mockup's: 24 characters in six groups of four, from a 32-character alphabet without look-alikes, with a check character if it fits. It comes from the system CSPRNG (`SecRandomCopyBytes`). Each box's extra restic key is derived from it with a KDF that includes the box ID. It's never written to disk or logs, and never leaves the app except through Print or Copy.
3. **Making boxes from the app:** the app makes the keys and stores them in the Keychain, then hands them to `portenvd` with a new `CreateBox` call that writes the config and sets up storage.
4. **"Save to Passwords" is dropped.** The step offers Print and Copy, and Continue needs "I've saved my recovery key somewhere safe".
5. **The deployment target stays macOS 26.** Nothing here needs more than CryptoKit's HKDF (macOS 11).

## Decision

### The recovery key's format

- **Alphabet:** `23456789ABCDEFGHJKLMNPQRSTUVWXYZ`. That's the digits 2 to 9 and every letter but I and O: 32 characters, so each is exactly 5 bits. With 0, 1, I and O gone, nothing can be mistaken for them. The mockup's sample uses only these characters.
- **Shape:** 24 characters, shown as six groups of four joined by hyphens (`K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5`). The first 23 are random. The 24th is a check character.
- **Strength:** 23 random characters give 115 bits. That's beyond any brute force: at 10^15 guesses a second it would take about 10^12 years, before restic's scrypt makes each guess cost about half a second. The check character costs 5 bits out of 120 and keeps the mockup's layout. Adding a 25th character instead would break the six groups of four.
- **Why a check character:** the key is typed from paper, maybe years later. Without a check, a typo looks the same as a wrong key: restic says the password is wrong, and the person can't tell a typo from losing their data.
- **Generation:** 23 bytes from `SecRandomCopyBytes`, each one reduced to its low 5 bits. That gives uniform symbols, since 256 is a multiple of 32. If `SecRandomCopyBytes` fails, first run stops with an error and never falls back to anything weaker.
- **The check character** is a parity symbol over GF(32), a one-symbol Reed–Solomon check:
  - the field is GF(2^5) with the polynomial x^5 + x^2 + 1; α = x (the value 2) is primitive;
  - each character's value is its index in the alphabet (v0 … v23);
  - v23 = Σ α^(23−i) · vi for i = 0 … 22, where the sum is XOR;
  - a key is valid when Σ α^(23−i) · vi over all 24 characters is 0.
- **What the check catches:**
  - every single wrong character, the check character included;
  - every swap of two different characters, adjacent or not, because the 24 weights α^23 … α^0 are distinct (α has order 31);
  - all but 1 in 31 of the remaining errors.
- **Reading a typed key:**
  - case doesn't matter, and spaces and hyphens are ignored;
  - any other character is refused, by name: "Recovery keys don't use the character 0.";
  - the wrong length gives "A recovery key has 24 letters and digits. This one has 23.";
  - a failed check gives "This recovery key has a typo. Check each group against your copy.";
  - the canonical form is the 24 characters, upper case, without hyphens.
- **Test vector:** `K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5` is valid. The mockup's `…-3NZE` isn't, since the mockup is only an illustration. Screenshots and renders use this vector, never a generated key.

### Each box's recovery password

- **KDF: HKDF-SHA256** (RFC 5869), with:
  - IKM: the canonical 24 characters as ASCII, check character included;
  - salt: the ASCII string `portenv recovery key v1`;
  - info: the ASCII string `portenv restic password v1`, a zero byte, then the box ID as ASCII (`box-` and 12 lowercase hex digits, checked against that pattern first);
  - L = 32 bytes.
- **The restic password** is those 32 bytes as 64 lowercase hex characters, the same encoding as today's random repository keys (`keys.NewKey`). restic reads it from a pipe, as it does every password (ADR 0005). It's added with `restic key add --host portenv-recovery`, so `restic key list` shows which key it is. That label isn't secret.
- **Why HKDF and not Argon2id:**
  - a slow KDF protects secrets that people choose and that can be guessed; this one is 115 random bits;
  - restic already puts every key password through its own calibrated scrypt, so an attacker holding the repository pays that cost per guess anyway;
  - HKDF is in CryptoKit (`HKDF<SHA256>`) and Go's standard library (`crypto/hkdf`), so the key path gains no new dependency in Swift or Go;
  - restoring many boxes on a clean Mac stays instant.
- **Deterministic per (recovery key, box ID), and different across box IDs.** The box ID isn't secret: it names the repository (`<storage>/boxes/<box-id>`). So on a clean Mac, the IDs come from listing the storage, and the recovery key alone opens every box.
- **Re-encrypt** (flow 3, later) writes a new repository, which needs a new box ID anyway (the old one is still there while snapshots are copied). The name stays, the recovery password changes with the ID, and a device that knew the old one learns nothing about the new one. The details are Re-encrypt's design.
- **Test vectors** (computed with a reference implementation while writing this ADR; the tests' vector file is the authority, and the Swift and Go implementations must both agree with it):

  | Box ID | Recovery password for `K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5` |
  | --- | --- |
  | `box-0123456789ab` | `b4eef5e9da4b5563a65819e2bd99ec3291f9da036e6688e6d6be20cd120d66af` |
  | `box-0123456789ac` | `32e479fe2dcc4a2fa0b28f9c62f85f88a130de773bf72434019522fa769465e9` |

### What the app keeps: a pool of pre-derived recovery passwords, never the recovery key

The app has to add a recovery-derived key to every box it makes later, without asking for the recovery key each time. It can't do that without the recovery key, or something with the same power, unless it derives the passwords ahead of time. Three options:

- **Keep the recovery key in the Keychain.** Rejected. The Keychain is encrypted storage on this Mac's disk. Reading it once would give a master key to every box the person ever makes, on every device, for good: changing it means re-keying every box. And it breaks "never written to disk".
- **Ask for the recovery key at every New Box.** Rejected. People would keep the key next to the Mac, which defeats "somewhere safe and separate from this Mac".
- **Pre-derive a pool (chosen).**
  - **Filling it:** while first run still holds the recovery key in memory (on Continue from the Recovery key step), the app picks 32 fresh box IDs (`box-` and 12 hex digits from `SecRandomCopyBytes`, the same format as `portenv init`'s). It derives each one's recovery password, and stores the 32 pairs as one Keychain item. Then it drops the recovery key.
  - **Where the pool lives:** service `dev.portenv.recovery-pool`, with a dev lane's suffix as for the repository keys. The item is never synchronised (`kSecAttrSynchronizable` off), and becomes `ThisDeviceOnly` when keys move to the data-protection keychain (ADR 0013, 1.8). Only the app reads it, never `portenvd`.
  - **Using it:** each new box takes the next entry, and its box ID is that entry's ID.
  - **Emptying it:** an entry is removed only after `CreateBox` succeeds. From then on the box's recovery password isn't kept anywhere on the Mac: the repository has the key, and the recovery key derives it again.
  - **When it runs out:** New Box asks for the recovery key once, in its own sheet: "Enter your recovery key to keep making boxes. Portenv doesn't keep it." That refills the pool and checks that the person still has the key. With 32 entries this is rare.
  - **After a suspected compromise:** the person can discard the pool in Settings › Protection and recovery key, and Re-encrypt discards it too. Refilling it needs the recovery key.

**The threat model, plainly.**

- **What's on this Mac's disk, in the Keychain:** each box's repository key (as today), and up to 32 recovery passwords for boxes not made yet. The recovery key isn't there.
- **Someone who can read this Mac's Keychain** can already open every box on this Mac with the repository keys. The pool adds the recovery passwords of the next boxes made from it, at most 32. While they keep that access, they'd get those boxes' repository keys anyway. If they lose it, the passwords still open those future boxes, as long as they can reach the storage. Discarding the pool closes that.
- **They never get the recovery key.** So boxes made on other devices (Phase 3), and boxes made after the pool is replaced, stay closed to them.
- **It isn't escrow:** nothing leaves the Mac, and nothing goes to iCloud.

**Later, not in 1.5:** a device enrolled by approval (Phase 3, flow 1) never sees the recovery key. Its pool comes from the approving device, wrapped to the new device's key, as part of that flow's design.

### Handling the recovery key in the app

- **Memory:** the recovery key exists only in the app's memory, from leaving Protection until the pool is filled. It lives in a byte buffer that's overwritten when it's dropped. SwiftUI's `Text` and the VoiceOver label make copies the app can't clear, so this is best effort, and stated as such.
- **Never written, never logged:**
  - not to a file, the defaults, a log line (`os_log` / `Logger` never takes it, even as a private value), a crash breadcrumb or the state restoration archive;
  - never sent to `portenvd`, which receives only derived passwords.
- **Copy** uses the existing `ConcealedCopy`:
  - concealed and transient types, cleared after 60 s if still there;
  - plus `prepareForNewContents(with: .currentHostOnly)`, so Universal Clipboard never carries it to another device.
- **Print** goes through `NSPrintOperation` with a view drawn on the spot. The app writes no file. macOS's print system spools the job briefly, and "Save as PDF" in the print panel is the person's own choice.
- **"I've saved my recovery key somewhere safe"** enables Continue (already built). Back and forward keep the same key. Quitting before Continue stores nothing, and the next first run makes a new key: the old one never protected anything.

### Making a box: `CreateBox`

**The proto (sketch; the PR settles field numbers and comments):**

```proto
// Makes a new box from keys the app made and stored in the Keychain
// (ADR 0019): writes no key to disk, creates the repository with the box's
// repository key, adds the recovery-derived key, checks both, and only then
// writes the box's config. Idempotent on box_id: calling again after a
// failure or a crash resumes where it stopped.
rpc CreateBox(CreateBoxRequest) returns (CreateBoxResponse);

message CreateBoxRequest {
  string name = 1;              // the box name rule (local.NameRE)
  string box_id = 2;            // from the app's pool: box- and 12 hex digits
  string image = 3;             // empty: the default toolbox
  string storage = 4;           // as BoxConfig: "" (this Mac only), sftp:…, s3:…
  string storage_host_key = 5;
  string storage_rest = 6;
  // Exactly the box's key IDs for this storage (local.KeyIDs), as in
  // ProvideKeys. Held in memory only, like ProvideKeys.
  map<string, bytes> keys = 7;
  // The recovery-derived restic password (64 hex characters). Used to add and
  // check the recovery key, then dropped: never stored, never logged.
  bytes recovery_password = 8;
}

message CreateBoxResponse {
  string box_id = 1;
  bool already_created = 2;     // an earlier call had finished it
}
```

- **Unfinished creations:** `ListBoxesResponse` gains a `creating` flag per box. The app finishes such a box with `CreateBox` (the same box ID, the keys from the Keychain, the recovery password from the pool), or discards it with `DiscardCreation(box_id)`.
- **Never logged:** the request is never logged, by `portenvd` or by any gRPC interceptor. Logs carry the method, the box name and ID, and the outcome only.

**The order, and what a failure at each step leaves.** The rule: keys are stored before anything is encrypted with them, and a box can save only once its config exists. The config is written last, after both keys have been shown to open the repository. Each step is safe to repeat.

| Step | Who | What | If it fails or crashes here |
| --- | --- | --- | --- |
| A1 | App | Take the pool's next entry (box ID and recovery password). The entry stays in the pool. | Nothing exists yet. |
| A2 | App | Make the repository key (32 random bytes, hex), and, for SFTP storage, the storage SSH key and the REST password. Write them to the Keychain under that box ID, and read each one back to compare. | No repository and no config exist. The app deletes what it wrote, best effort. Leftover keys with no saves are harmless. |
| D1 | `portenvd` | Check the request: the name rule, the box ID pattern, the key IDs exactly as `local.KeyIDs` gives for the storage, non-empty keys, and storage that can be reached. Put the keys in its in-memory store. | Nothing is written. The error goes back to the app, which deletes the Keychain items as in A2. |
| D2 | `portenvd` | Write a pending record, `state/<box-id>/creating.json`, holding the request without keys, atomically (a temporary file, fsync, rename). The name is now reserved. `LoadBox` and `OpenBox` don't see it, and `ListBoxes` shows it as `creating`. | No repository exists. The retry starts again here. |
| D3 | `portenvd` | Create the repository with the repository key, on the host, with ADR 0012's deadline. If a repository already exists at that path, it's used only if the repository key opens it. Otherwise: FailedPrecondition, and the repository is never touched. | A repository may exist with the repository key only, and no snapshot. No config exists, so nothing can save into it. A partial `init` that leaves the repository unopenable fails the retry with FailedPrecondition. The app then discards the creation and uses the pool's next entry. The empty leftover is never deleted automatically. |
| D4 | `portenvd` | If the recovery password doesn't open the repository yet, add it: `restic key add --host portenv-recovery`, current password the repository key, new password on fd 4. | The repository still has only the repository key, and no snapshot. The retry repeats D3 (it opens) and D4. |
| D5 | `portenvd` | Check: the repository opens with the recovery password alone, and with the repository key alone (`restic cat config` with each). | As D4. A recovery key that was added but doesn't check out fails with Internal and is never committed. |
| D6 | `portenvd` | Write `boxes/<name>.json` atomically, with `"recovery": true`, a flag that isn't secret. Then remove the pending record. Drop the recovery password from memory. | The repository has both keys and no snapshot. The retry skips D3 to D5 (both keys open it) and writes the config. |
| A3 | App | Remove the entry from the pool, then open the box (`OpenBox`). The first save happens there, as for any new box. | If the app crashes before removing the entry, it removes, at next launch, every pool entry whose box ID `ListBoxes` reports as a box. |

- **Recovering from a crash:** the pool entry stays until the box exists, so after a crash of the app or `portenvd`, the app can finish a half-made box at next launch without asking for the recovery key.
- **The box ID is the idempotency key:**
  - `CreateBox` for a box ID already committed, with the same name and storage, returns `already_created`;
  - with anything different, it returns FailedPrecondition.
- **Errors**, each one plain line that never contains a key (GUIDELINES §10):
  - InvalidArgument: a bad name, box ID or key set;
  - AlreadyExists: "A box named acme-api already exists.";
  - FailedPrecondition: a repository at that path doesn't open with these keys, or the box ID is taken by different settings;
  - Unavailable: the storage can't be reached (retry);
  - DeadlineExceeded: restic missed its deadline (ADR 0012);
  - Internal: anything else.

**What this guarantees.** A box's first save happens only through `OpenBox`, and `OpenBox` needs the config, which D6 writes only after D5. So a box the app made never has a save without both its repository key (stored in the Keychain in A2) and its recovery key (in the repository since D4, and checked in D5). The Delete-a-box invariant holds at every step.

**Storage kinds in 1.5.** `CreateBox` is built and tested first for This Mac only, the only kind whose setup needs nothing else. For SFTP, the storage key still has to be authorised on the server and its REST user added (today `portenv init` prints both); that waits for the setup flows (2.9's wizard). For S3, where the bucket's credentials live is an open question.

### Boxes made before this ADR, and the CLI

- Boxes made with `portenv init` (Phase 0, and dev boxes) have no recovery key.
- Settings › Protection and recovery key lists them ("acme-api has no recovery key"), with "Add Recovery Key…". That asks for the recovery key, derives each box's password, and calls `AddRecoveryKey(name, recovery_password)`, which runs D4 and D5 for an existing box and sets the config's flag.
- A Mac with boxes but no recovery key yet (the owner's dev Macs, which skip first run) gets "Make a Recovery Key…" in the same place. It shows the first-run step, fills the pool, and adds the key to every box.
- `portenv init` stays a dev and terminal tool. When the terminal edition gets its own recovery key flow, it uses the same format, KDF and vectors (the Go implementation below).

### Restoring with only the recovery key

- **On a clean Mac**, with no Keychain items and no Portenv state, the person gives the storage location and the recovery key. The app lists `<storage>/boxes/`, derives each box's recovery password, and calls `RecoverBox(box_id, storage, recovery_password, keys)`.
  - **In that call,** `portenvd` opens the repository with the recovery password, adds a new repository key for this Mac (made by the app and stored in the Keychain first, as in A2), checks it, and writes the config.
  - **The box** then opens like any other, under resume rules 2 to 5.
- **Where the app offers this** (for example "Restore with a recovery key" on Welcome, and in Settings) needs a board. That's an open question for the owner. The call and its test come with `CreateBox`.

### Implementations

- **Swift (`PortenvKit`):** the format, the check, reading a typed key, the derivation and the pool. Used by the app.
- **Go (`core/keys/recovery`):** the same format, check and derivation. It serves the Go restore test, `portenvd`'s check of the box ID pattern, and the terminal edition later. `portenvd` never derives anything: it receives passwords.
- **Both run against one vector file,** `core/keys/recovery/testdata/vectors.json`, which the Swift tests read too.

## Tests (written before the code)

1. **Format and alphabet:**
   - the alphabet has 32 distinct characters, and none of 0, 1, I or O;
   - generated keys are 24 characters, grouped 4-4-4-4-4-4, and always pass their own check;
   - formatting and reading round-trip: lower case, spaces and missing hyphens all read back to the canonical form;
   - generation uses only the low 5 bits of each CSPRNG byte (a fake byte source gives a known key).
2. **The check character:**
   - exhaustively over the vector key and over 1,000 random keys, every single-character change and every swap of two different characters is caught (zero misses);
   - a two-character error is caught at the expected rate, measured;
   - each error message is right: unknown character, length, typo.
3. **The derivation:**
   - the same (key, box ID) always gives the same password;
   - box IDs that differ by one character give different passwords;
   - different keys give different passwords;
   - a box ID outside `^box-[0-9a-f]{12}$` is refused;
   - the output is 64 lowercase hex characters.
4. **Known-answer vectors** (the file above): Swift and Go both match every vector, including the two in this ADR, and the HKDF implementation matches RFC 5869's test case 1. Mutation check: changing the salt, the info's separator or the order of fields fails the vectors.
5. **Restore with only the recovery key, on a clean Mac** (Go, against real restic, in temporary directories):
   - make a repository through `CreateBox`'s steps with a file key store (`PORTENV_KEYS=file`) and a fixed recovery key, then save a home with known files;
   - delete the key store, the state directory, the box config and restic's cache, and start a fresh `Env` (new `PORTENV_HOME`, empty key store);
   - read the recovery key from its printed text form, list `boxes/` in the storage, derive, and `RecoverBox`;
   - restore and compare checksums of every file;
   - control: a key with one wrong character is refused by the check before restic runs, and a different valid key fails to open the repository.

   The release's recovery drill (PLAN.md, Testing) repeats this on a real clean Mac with the app.
6. **`CreateBox` failing at each step** (Go, real restic, with a fault-injecting seam per step: D1 to D6, a crash between steps through a killed process, and A2/A3 on the app side with a fake key store):
   - after every failure and every crash, no repository has a snapshot unless the box's config exists and both keys open it;
   - `OpenBox` refuses a box that's still `creating`;
   - retrying with the same box ID ends in one working box, and never adds a second recovery key;
   - a repository already at the path that doesn't open with the given keys is left byte-for-byte untouched;
   - `already_created` comes back on a repeat call, and FailedPrecondition when the settings differ;
   - the app removes a pool entry only after success, and reconciles at launch.
   - Each check is mutation-checked (for example, writing the config before D5 must fail the test).
7. **Nothing secret in logs** (log capture):
   - run first run's key steps, `CreateBox`, `AddRecoveryKey` and `RecoverBox` with known keys;
   - scan for the recovery key (with and without hyphens, in either case), every derived password and every repository key, in:
     - `portenvd`'s log, its stdout and stderr, and restic's captured stderr;
     - the app's own `OSLogStore` entries for the test process;
     - the Portenv directory (configs, pending records, state);
     - the defaults, and the saved application state;
   - any match fails.
8. **The recovery key stays in the app:**
   - no gRPC message type has a field that could carry the recovery key itself, only `recovery_password`;
   - `portenvd`'s code never imports the derivation (a boundary test, like the engine boundary).
9. **The pool:**
   - it's filled with 32 entries from one recovery key, and each entry matches the derivation;
   - discarding it works, and refilling needs the key;
   - the Keychain item is written with synchronisation off;
   - the recovery key isn't in the item.

## Consequences

- **PLAN.md** changes in the same commit:
  - Open questions: the default mode, the recovery key, CreateBox and Save to Passwords are answered with a pointer here;
  - the Keys list says how the recovery key becomes each box's key;
  - first run's Recovery key step loses Save to Passwords;
  - 1.5's "Not yet" points here.
- **Two new open questions for the owner:** where the app offers restoring with a recovery key, and where S3 credentials live.
- **The mockup board** `first-run-3-recovery-key` still shows Save to Passwords. The app doesn't build it, and the board's sample key isn't a valid key; screenshots use the vector key.
- **New calls on `portenvd`:** `CreateBox`, `AddRecoveryKey`, `RecoverBox` and `DiscardCreation`, and `ListBoxes`' `creating` flag. Each is a data-loss path, with its tests first.
- **New Box** sometimes asks for the recovery key: when the pool of 32 runs out. That's the price of never keeping the recovery key on the Mac.
- **Re-encrypt and Phase 3's device enrolment** each need a line on the pool when they're designed: Re-encrypt discards it, and an approved device gets its own from the approving device.
