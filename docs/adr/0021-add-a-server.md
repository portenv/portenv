# 0021. Add a Server: pinned host keys, per-server keys in memory, and a setup that changes nothing until every check passes

Date: 2026-10-13 · Status: Accepted (the reviewer, on the owner's delegation, R-0140, R-0143). Amended 2026-10-14 (R-0180, R-0183): see "1a. Host keys, as built" below, which wins where it differs.

## Context

Phase 2 item 2.9 replaces the Phase 0 path to a server (run `server/setup.sh` by hand as root, then add `user@host` to `machine.json`'s `servers`) with a wizard in the app: the `add-a-server` board (R-0136, R-0141). Testers must reach a server without Terminal, and lane E's tester servers use the wizard as their only setup path.

Today every SSH call goes through `core/local/remote.go`'s `sshBase`: the system `ssh`, the person's own `~/.ssh/config`, `~/.ssh/known_hosts` and keys, with a ControlMaster socket in `/tmp/portenv-<uid>`. So which host key is trusted, and which key signs in, is whatever the person's SSH setup says. That's fine for the owner's own servers; it isn't something the app can show, pin or explain.

The rules this ADR keeps:

- **No public or inbound ports** (CLAUDE.md; ADR 0015's public doors are opt-in per server and not part of this). Everything goes through the server's existing SSH.
- **No plaintext keys or secrets on disk.** Keys live in the Keychain; `portenvd` holds them in memory only, handed over by the app (ADR 0013, `ProvideKeys`).
- **Only the app raises Keychain prompts** (ADR 0013).
- **Agents never get capability through the network** (ADR 0007): nothing a workspace or an agent can reach may sign in to a server.

## Decision

### 1. Host keys: Portenv's own `known_hosts`, confirmed once, pinned after

- **One file**, `known_hosts` in Portenv's support folder (`Application Support/Portenv/` or a dev lane's own), mode 0600. Every `ssh` Portenv runs uses it with `-o UserKnownHostsFile=<it> -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UpdateHostKeys=no`, and never the person's `~/.ssh/known_hosts` for trust. Only Trust and Continue and Forget This Server's Key change Portenv's file; ssh itself never does (`UpdateHostKeys=no`).
- **The key type is pinned, not just the key.** First contact runs `ssh-keyscan -t ed25519` (bounded, ADR 0012), and falls back to `ecdsa`, then `rsa`, only when the server has no Ed25519 host key. Portenv stores that type with the key and passes `HostKeyAlgorithms=<that type>` on every run, so a server offering another type can't make ssh choose differently.
- **First contact** then shows the fingerprint (SHA256, the key type named): "Is this the right server?". Only **Trust and Continue** writes it to Portenv's file. Portenv test servers list the fingerprint in the invitation (R-0140); a self-hosted person compares it with what their provider shows ("Where do I find this?").
- **Existing servers** (`machine.json`'s `servers`, Phase 0): the matching line in `~/.ssh/known_hosts` may **pre-fill** the fingerprint, read-only (Portenv never writes there), but the person still confirms it once, on the next contact, in the same frame. Until then, that server isn't contacted. So that a dev server's saves don't stop silently, the first launch after this change shows **one alert listing every server that needs confirming**, with a way to each one's fingerprint frame.
- **A changed host key is a hard stop.** Nothing is sent: no command, no key, no save. The alert says the server's identity changed and why that happens (a reinstalled server, or someone in between), and it has **no "trust anyway"**. The only way past is **Settings › Servers › Forget This Server's Key**, which says the same sentence again and then sends the person back through the fingerprint frame. Workspaces on that server stay where they are, unopened, until then.
- **Tests:** a changed key refuses every call (the open, a save, Move To, Check, the runner's channel); pre-fill never writes `~/.ssh`; a server not yet confirmed is never contacted.

### 1a. Host keys, as built (amended 2026-10-14, R-0180, R-0183)

Where this differs from section 1 and the tests below, this wins.

- **Pinned by alias.** Portenv's `known_hosts` has one line per server, `portenv-<server id> <type> <key>`, mode 0600 in the 0700 support folder (tested). Every run also passes `-o HostKeyAlias=portenv-<server id> -o CheckHostIP=no`, so the pin holds however the address resolves (an alias, a port, a new IP). Portenv's options come first on the command line, before `PORTENV_SSH`'s and the config's: ssh keeps the first value it's given (tested).
- **Found through ssh itself, never `ssh-keyscan`,** which ignores `~/.ssh/config` (HostName aliases, Port, ProxyJump, ProxyCommand) and so can scan the wrong host. `ssh -G` resolves the address; then `ssh -o UserKnownHostsFile=<a fresh file in <T>pv/> -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PreferredAuthentications=none -o HostKeyAlgorithms=<the order> -o HostKeyAlias=portenv-<id> -o ControlPath=none <host> true` records the key and fails to sign in; Portenv reads it from that file and deletes the file. Bounded (ADR 0012). Made-key servers do the same with `-F none` (1b). Tested through a ProxyJump alias against real `sshd`s, run as the test's user on 127.0.0.1, on the Mac and in CI's Go job (not a container).
- **Key types:** Ed25519, then ECDSA, then RSA as `rsa-sha2-512,rsa-sha2-256`, never `ssh-rsa` (SHA-1). A host offering only a certificate is told so and refused. The stored type gives `HostKeyAlgorithms` on every run.
- **The server list** moves to `servers.json`, made once from `machine.json`'s list, which is left in place, unread, for one release, so an older build still works.
- **Pre-fill from `~/.ssh/known_hosts`,** read only (`ssh-keygen -F <name> -f <file>`, plain or hashed):
  - the same key confirms the server silently; its info says "Confirmed from this Mac's SSH known hosts";
  - a different key of that type is a changed key, with no Trust: "This server's key doesn't match the one this Mac's SSH recorded. Check with whoever runs it.";
  - no entry: the alert offers Trust, with the fingerprint.
- **Until it's confirmed,** a server isn't contacted, and its workspaces say "Confirm <name> to open this." Instead of one alert listing every server, a plain alert at launch (at most once per launch) asks about each server waiting: Trust or Not Now.
- **Until Forget is in the app** (Settings › Servers or the wizard), the changed-key line names the owner's fallback: `portenv app remove-server <name>`.
- **For the owner's scripts only:** `portenv app trust-server USER@HOST "TYPE KEY"` pins a key given by a script that set the server up (no one looks at it), and `portenv app remove-server` drops a server. Both are refused inside a workspace (the box's environment marker), so a workspace's agent can't pin a server for the owner.
- **The ControlMaster socket** is `<T>pv/c-<the server ID's 12 hex digits>`, not `c-%C`: %C's 40 hex digits, with the 17-character suffix ssh adds while it makes the socket, run past 104 bytes (found by the real-sshd test).
- **`ControlPersist=600`:** the shared connection outlives its last use by 10 minutes at most. While it's up, any process of this user can reach the server through the socket with no Keychain prompt, so the window stays short; 10 minutes still covers an autosave cycle without signing in again each time.

### 2. The per-server key, served by an in-memory agent

The board's "Sign in with": **A key on this Mac** (the person's existing key or agent, as today) or **Make a key just for this server**.

- **Making one:** the app makes an Ed25519 key with CryptoKit and stores the private half in the Keychain, then shows the public half (OpenSSH format) with Copy, to paste into the provider's SSH keys or `authorized_keys`. One key per server; it signs in to that server only. It's for SSH login only: storage keys stay ADR 0019's (R-0140).
- **The Keychain item:** generic password, service `dev.portenv.server-key` (with a dev lane's suffix, as for the repository keys), account the server's ID, `kSecAttrSynchronizable` off and `WhenUnlockedThisDeviceOnly`. **ADR 0019's caveat applies as stated there:** until the move to the data-protection keychain at 1.8, the file-based login keychain ignores `kSecAttrAccessible`, so the item travels in backups and Migration Assistant like any login keychain item. The key's comment is `portenv-<server id>`: no user name, host name or email.
- **Handing it to `portenvd`:** like `ProvideKeys`, over the owner-only socket, held in memory only; never written, never logged.
- **ssh's own config:** a server with a made key runs ssh with **`-F none`** and only Portenv's explicit options, so the person's `~/.ssh/config` (a `Match exec`, a `ProxyCommand`, another `IdentityFile`) has no part in it. **"A key on this Mac"** keeps the person's config, since that's why they chose it (ProxyJump and the like), but the trust options above are still forced on the command line, where they win over the config.
- **The agent:** for each `ssh` run that needs the key, `portenvd` starts an SSH agent of its own (Go's `golang.org/x/crypto/ssh/agent`, in-process) on **one socket for that run**, which **serves only that server's key**, refuses to add or remove keys and to sign for anything but that run, and **closes when the run ends**.
  - **Where the socket lives:** the per-user temp folder, read with `confstr(_CS_DARWIN_USER_TEMP_DIR)` (never the `TMPDIR` variable), in a folder `pv/` created 0700, owner and mode checked before every use: `<T>pv/a-<8 hex>`, random per run. macOS caps a socket path (`sun_path`) at 104 bytes and `<T>` is already about 50, so the names stay short, and a test asserts the longest path fits.
  - **The ControlMaster socket moves there too,** in the same PR: `<T>pv/c-%C`. Today's `/tmp/portenv-<uid>` is in a folder anyone can create first (a squatting race).
  - **How they fit:** a run that finds no master starts one; the master authenticates once, through that run's agent; once the master is up, the agent socket closes. Later runs reuse the master and need no key. A master that dies is started again the same way.
  - **Nothing a workspace mounts is there:** boxes get their home volume, the storage folder (`Repositories`) and restic's cache volume, and that list is checked by a test.
  - **ssh options, set explicitly:** `IdentityAgent=<that socket>`, `IdentitiesOnly=yes`, `IdentityFile=none`, `ForwardAgent=no`, and `ClearAllForwardings=yes` except where a run needs the REST forward (ADR 0005's existing `-L` to the loopback REST server).
  - **No workspace or agent ever reaches it:** not mounted, not forwarded, not in any box's environment. A test starts a box and an agent session while a run is live, and checks neither can see the socket or `SSH_AUTH_SOCK`.
- **"No SSH yet? Copy an install command…"** (PLAN.md's alternative path): a one-line command for the provider's web console or user-data that only adds this server's public key to the user's `authorized_keys`. It installs nothing; Check and Set Up still run from the wizard, so nothing installs before every check passes.
- **"Someone is setting one up for me"** (R-0140, R-0141): the same key, made at once, its public half shown with Copy and "Send this to the person setting up your server. It's safe to share."; **Done** saves a Settings › Servers entry, "Waiting for an address". Opening it later ("The invitation arrived") asks for name, address and user, says "Signs in with: the key you sent on <date>", and continues at the fingerprint frame. "A key on this Mac" isn't offered there: testers may not have one, and it would share one key across everything they reach.

### 2a. The per-server key, as built (2.9 1b)

Where this differs from section 2, this wins.

- **The key:** CryptoKit's Ed25519; the Keychain holds its 32-byte seed (service `dev.portenv.server-key`, a lane's own suffix; account the server ID; label `Portenv server key portenv-<id>`; never synchronised, this-device-only, with ADR 0019's caveat). Its public half is `ssh-ed25519 … portenv-<id>`. At launch the app reads the seeds of the servers that sign in with one and hands them to `portenvd` (`ProvideServerKey`), which holds them in memory only (tested: nothing in Portenv's folder or the log carries one). Without it, a run says "Portenv needs <name>'s key from the Keychain. Open Portenv on this Mac to allow it."
- **The run agent** (`<T>pv/a-<8 hex>`, 0600) serves that key only, signs for nothing else, refuses add, remove, remove-all and lock, and is closed (socket gone) as soon as the shared connection it signed in is up. Later runs go through that connection with no key at all.
- **`IdentityFile` names the key's public half,** `<T>pv/a-<8 hex>.pub`, beside the agent's socket and removed with it, instead of `none`: with `IdentitiesOnly=yes`, ssh offers an agent's key only when an `IdentityFile` names it (found by the real-sshd test). The private half is never on disk. Runs through the shared connection pass `IdentityFile=none` and `IdentityAgent=none`.
- **A made-key server's command line:** the ssh binary only from `PORTENV_SSH` (its options, a later `-F` among them, would win over `-F none`), then `-F none`, the pins, `ProxyCommand=none`, `KnownHostsCommand=none`, `PermitLocalCommand=no`, `IdentitiesOnly=yes`, `IdentityFile`, `IdentityAgent`, `ForwardAgent=no`, `ClearAllForwardings=yes` (`no` only for the REST forward, ADR 0005) and `ControlMaster`/`ControlPath`. Scanning it uses `-F none` too. Tested against a real `sshd` with a planted config (`Match exec`, `ProxyCommand`, `LocalCommand`): nothing in it runs.
- **Nothing a workspace mounts is there:** the driver's test checks a box mounts its home volume, the storage folder and its cache volume, and nothing else; the socket folder is outside the person's home and Application Support (tested).

### 3. Check never changes the server

One read-only probe, run over SSH as the given user, bounded (ADR 0012), with each line answering one question:

- the OS and version (Ubuntu 22.04 or 24.04, Debian 12) and the architecture (arm64 or amd64);
- memory and free disk against the minimums;
- **sudo:** the user is root, or `sudo -n true` succeeds. Portenv **never asks for or stores a sudo password**; without passwordless sudo, Check says so with the fix (add the user to sudoers with `NOPASSWD`, or use root) and stops;
- the clock is within 2 minutes of the Mac's (certificates, restic's lock times);
- outgoing HTTPS to `ghcr.io` and `download.docker.com` (the toolbox image and Docker's repository);
- room to run workspaces: Docker Engine already there (and its version), or installable; an existing Portenv install and its version;
- KVM availability, recorded for later microVM support (PLAN.md), never a failure.

Check writes nothing on the server: no package, file, user or setting. The probe is a fixed script sent on stdin, reviewed with this ADR, and a test runs it against a server whose file system is compared before and after. Each failure says what to do in one line (GUIDELINES §10), for example "Portenv downloads the workspace toolbox from ghcr.io over HTTPS. Allow outgoing HTTPS (port 443) in your provider's firewall, then choose Check Again." **Nothing installs until every check passes.**

### 4. Set up

- **What it does:** uploads `portenv` and `portenv-runner` for the server's architecture (the release's own), checked against SHA-256 checksums from **a manifest inside the signed app bundle**, never fetched alongside the binaries, then runs `server/setup.sh` with sudo, which already: installs Docker Engine from Docker's repository, makes the encrypted volume for workspace homes, the SFTP-only storage account for saves and the loopback-only REST server, and opens no port. Then it starts a test workspace, checks its terminal and its save, and removes it.
- **Everything Portenv adds is named `portenv-*`** and sits beside what's there.
- **sudo after Set Up:** setup needs root or full `sudo -n` once. Day to day, Portenv runs only `sudo -n /usr/local/bin/portenv …` on the server (2.1's rule), never a shell; Portenv doesn't edit the person's own sudoers.
- **The test workspace** checks a terminal and a save; it adds the port relay check once 1.6 ships (PLAN.md's step 5).
- **`portenvd` owns the job,** not the window: closing the wizard doesn't stop it; it reports through a notification when it ends, and Settings › Servers shows it meanwhile. Stop Setup stops after the step running, and says what's left on the server.
- **No new ports**, checked after Set Up by the same probe (listening sockets before and after).

### 5. "Turn off password logins": offered, never forced

- **A firewall** (PLAN.md's other optional hardening) isn't in v1: nothing Portenv adds listens outside the loopback address, so there's nothing for it to close. It can come later, offered the same way.
- **Unticked by default** on servers Portenv didn't create (Portenv-hosted servers get it from cloud-init), with "Portenv proves your key works first and undoes it if sshd refuses" (R-0141).
- **How:** a drop-in, `/etc/ssh/sshd_config.d/01-portenv.conf`, with `PasswordAuthentication no` and `KbdInteractiveAuthentication no`. sshd takes the **first** value it reads, and reads the drop-ins in lexical order, so the name sorts before Ubuntu cloud images' `50-cloud-init.conf` (which says `PasswordAuthentication yes`).
- It's validated with `sshd -t` before reload. After the reload, the **effective** values are read with `sshd -T` (`passwordauthentication no`, `kbdinteractiveauthentication no`). Then a **second** SSH connection proves key login while the first one stays open.
- If `sshd -t` refuses, the effective values aren't both `no`, or the second connection fails, the drop-in is removed and sshd reloaded through the first connection, and the wizard says so.

### 6. Remove a Server

- Every workspace on the server is saved and moved back to this Mac first (Move To this Mac); one that can't be moved stops the removal, saying which.
- Then Portenv removes only what it added (`portenv-*`: the runner, its user and service, the CLI, the storage account, the homes volume once empty). Docker and the person's own things stay.
- **Saves stored on that server** (its storage account) are never deleted as part of removing it. Deleting them is a separate, explicit choice, offered only after **each** one is confirmed copied elsewhere (its newest save present and checked in the other storage); otherwise the saves stay and the server is listed as "Saves only".

### 7. Docker's signing key, pinned

`server/setup.sh` pins Docker's release signing key by fingerprint (`DOCKER_KEY_FINGERPRINT`) and checks it before adding the repository; a different key stops setup. This stays, and a test checks that every fingerprint in the downloaded key (primary and subkeys) is the pinned one or a subkey of it.

## Tests (written before the code)

1. **Host keys:**
   - first contact writes Portenv's file only after Trust and Continue;
   - a changed key refuses every kind of call and offers no way past but Forget This Server's Key;
   - pre-fill reads `~/.ssh/known_hosts` and never writes it;
   - an unconfirmed server is never contacted, and the first launch's alert lists every server waiting;
   - `ssh-keyscan` asks for Ed25519 first and falls back only when there's none; every run carries `HostKeyAlgorithms=<the stored type>` and `UpdateHostKeys=no`;
   - a planted `~/.ssh/config` with `Match exec` runs nothing for a made-key server (`-F none`).
2. **The agent:**
   - it serves only its server's key, refuses `add`, `remove` and `lock`, and its socket is gone when the run ends;
   - the folder is 0700 and owner-checked (a wrong mode or owner refuses the run), it's read with `confstr`, not `TMPDIR`, and the longest agent and control socket paths fit in 104 bytes;
   - the agent socket closes once the master is up, and a later run reuses the master without a key;
   - the ssh command line carries `IdentitiesOnly=yes`, `ForwardAgent=no` and `IdentityAgent`;
   - a box and an agent session started during a live run can't see the socket.
3. **The key:** never on disk or in a log (the scan from ADR 0019's test 7); the Keychain item's attributes; the comment is `portenv-<server id>`.
4. **Check:** writes nothing (the before-and-after comparison); each failing line's fix text; sudo without `-n` stops Check.
5. **Set Up:** binaries that don't match the bundle's manifest are refused; installs only after Check passes; opens no port; carries on with the window closed and notifies; Stop Setup stops after the step running.
6. **Hardening:** off by default; with a planted `50-cloud-init.conf` saying `yes`, `01-portenv.conf` still wins (`sshd -T` reads `no`); `sshd -t` failure, an effective value that isn't `no` and a second-connection failure each roll back.
7. **Remove a Server:** workspaces come back first; saves on the server stay unless the separate choice is made after each is confirmed copied.

**Where they run:** Check against an sshd container in CI. Set Up against the CI runner's **own VM over localhost sshd** (real systemd and a real Docker install, which a container can't fake). One real run on a lane E server before testers, when the owner's AWS terms allow it (R-0140).

## Consequences

- `core/local/remote.go`'s `sshBase` gains Portenv's `known_hosts`, the pinned key type and, per server, the agent options (and `-F none` for a made key); the ControlMaster folder moves to the per-user temp folder. The owner's dev servers go through the fingerprint frame once, announced by one alert at first launch.
- `machine.json`'s `servers` list moves to Settings › Servers' records (name, address, user, server ID, the pinned key, how it signs in, and its state), migrated once.
- New `portenvd` calls for Check, Set Up (with a progress stream) and Remove; the key arrives as `ProvideKeys` does.
- PRs, in order: this ADR; pinned host keys and the agent; Check and Set Up; the wizard (with R-0141's frames); Settings › Servers (in lane D's Settings window); Remove a Server.
- PLAN.md's 2.9 points here.
