# 0025. New workspace: project, team and review in one flow; saved teams; a runner registry; GitHub clone and push from the Mac's sign-in

Date: 2026-10-18 · Status: proposed (the owner's request, relayed in R-0318, with his clarifications R-0319, R-0320 and R-0321, and his GitHub decision R-0326). Builds on ADR 0022 as amended by PR #198 (each coding agent in its own clone, section 2a). ADR 0023 is #156's keep-running; 0024 is reserved for network rules.

**The owner's GitHub decision (R-0326):** no GitHub App for the tester build. The sign-in is a normal GitHub OAuth sign-in with the device flow: its token has the account's `repo` scope (read and write on every repository the account reaches) and no expiry of its own, so Portenv narrows where it goes instead of what it can do (section 6). A GitHub App, for repository-scoped access (teams, organizations), is a later option; ADR 0018's choice stands for now. **One owner step before code:** registering Portenv's OAuth app on GitHub is publishing, and the owner's to do; this ADR doesn't assume it's done.

## Context

Today a team arrives in a workspace by hand: the owner makes the workspace, runs `portenv team init --template starter|orchestrate` (ADR 0022 §4b) in a terminal, then approves each agent file in the Team sidebar, one by one, before the first Start. The owner wants this inside New workspace, with no terminal (R-0318), and wants to reuse a team he has tuned, as a template he saves (R-0318), with each agent's runner and model swappable without touching its instructions or grants (R-0319). New workspace also has to bring a project in from GitHub, a Git URL or a folder on the Mac (R-0320), and the owner must be able to push from the workspace for testers (R-0321).

What's true today, and decides the design:
- **New workspace** (`new-box` board) asks for a name, a start (GitHub repository, folder on this Mac, empty) and a toolbox. GitHub works by ADR 0018: `gh auth login` inside the box, the token in the box home's `hosts.yml`, and agents in the box can use it. "Folder on this Mac" isn't built; PLAN's 1.7 holds the copy-in, and tonight's workaround is `docker cp`.
- **Templates** live in `core/internal/teamkit/templates/` (`starter`: lead, builder, reviewer; `orchestrate`: lead, builder, reviewer, design). Every file says `runner: claude`, with no model.
- **The runner table** is `core/internal/idleinput`, read by `core/daemon/runners.go`: an idle-input rule (a pattern and how many of the last lines it's tried on) keyed by the basename of the runner's first word (`claude`, `codex`), used for routines (ADR 0022 §4b) and the wake on a message (CC-0498, #195). A runner not in the table gets neither, with a plain line.
- **`home_seed` is dropped on the floor.** `portenvd` validates it and sends it in `StartTeamAgentRequest.home_seed`; the helper never reads the field, so nothing is copied.
- **`share_login` is a no-op.** `portenvd` parses it and carries it on the agent; no decision is raised and the helper copies nothing.
- **Approval pins a file by SHA-256** (ADR 0022 §3), after the file exists in the project; until then the team can't start.
- **Agents never write the owner's repository** (ADR 0022 §2a, #198): each has its own clone, and its work comes back as a bundle `work`'s git fetches.

## Decision

### 1. New workspace becomes five steps

The reviewer drew steps 2 to 5 as four artboards in its "Portenv Team Window" canvas (R-0322): **Project**, **Team**, **Review and approve** and **Creating**; the repository mockup `new-box-team` draws the same four, and the names below are theirs. **Words:** people see "starter folders" and "starter files" for what agent files call `home_seed` and this ADR calls seeds (R-0322).

One sheet, titled "New workspace", with the steps named at the top ("Name · Project · Team · Review and approve"), Back and Cancel on every step, and Continue (the default button) until the last. First run's "Make your first workspace" uses the same steps.

1. **Name and where** (as today): the name with its rule, and This Mac or a server.
2. **Project** (section 2): From GitHub, From a Git URL, From a folder on this Mac, or Empty; optionally, Mac folders to seed agents with.
3. **Team** (section 3): pick a team, then adjust it.
4. **Review and approve** (section 4): every agent file, in full, with its grants, and a summary of the project's source.
5. **Creating** (section 5): progress, with Cancel while the project is fetched; then the workspace opens.

**Create Workspace** is the last step's button. It stays disabled, and says why under it, until every included agent is approved. With Team set to None, Review shows only the project's summary.

### 2. The Project step

**From GitHub:**
- Sign in once (section 6); the step then shows a searchable list of the repositories the sign-in can reach (owner/name, private or public, last pushed), and a branch picker (the default branch first).
- Clone the whole history of the chosen branch's repository, checked out on that branch, into `/home/work/<workspace>` (ADR 0022 §3's project folder). Submodules aren't cloned in this build; the summary says so when `.gitmodules` exists.
- **The size is known before the clone:** GitHub's repository API gives `size` (in KB, the packed repository). `portenvd` refuses before fetching when `size × 2` (the pack plus a checkout), plus one full clone per coding agent (ADR 0022 §2a), plus 1 GB of room, is more than the workspace's free space, or when one agent's clone wouldn't fit its home limit: "acme-api needs about 7.4 GB with its team (3 copies of 2.1 GB). This workspace has 5 GB free." The check is repeated while bytes arrive, so a wrong estimate stops the clone instead of filling the disk.
- **Git LFS isn't supported in this build.** The clone keeps the pointer files, the review summary says "Uses Git LFS: large files come as pointers for now.", and nothing runs `git lfs`. Later, an LFS fetch through the same credential path (section 6).

**From a Git URL:** public `https://` URLs only in this build. The helper clones as `work` with `-c protocol.allow=never -c protocol.https.allow=always`, no submodules and no LFS. There's no size API, so the check runs only while bytes arrive. A URL that needs a login fails with "This repository needs a sign-in. Use From GitHub, or a folder on this Mac." Other hosts' sign-ins are an open question.

**From a folder on this Mac** (PLAN 1.7's copy-in, moved up; it replaces the `docker cp` workaround):
- The person picks the folder in the open panel, so the app, which has that consent, reads it and streams it to `portenvd`; `portenvd` never walks the Mac's disk by itself.
- **Before copying:** the app walks the folder (never following a link) and shows its size and file count. "Leave out installed dependencies (node_modules and similar). They're rebuilt in the workspace." is ticked by default (`node_modules`, `.venv`, `venv`, `target`, `.build`, `DerivedData`, `__pycache__`, `.DS_Store`), since programs built for the Mac don't run in the workspace. The size check is the GitHub one.
- **The copy:** the helper writes as `work` into `/home/work/<workspace>`, new files only, through an `openat` walk with `O_NOFOLLOW`. Regular files keep their content and their execute bit; set-uid, set-gid and sticky bits are dropped. A symbolic link is copied as a link only if it's relative and stays inside the folder; any other link, and every special file (device, socket, FIFO), is skipped and listed in the result. Progress is by bytes.
- **`.git` is copied, with two things left out and one cleaned:** `.git/hooks/` is never copied (Mac hooks don't belong in the workspace, and a fresh `hooks/` comes from the workspace's git); `.git/config` is rewritten to keep only `[core]`'s basic keys (`repositoryformatversion`, `bare`, `logallrefupdates`, `ignorecase` set to false), `[remote "…"]` with `url` (any user and password in it removed) and `fetch`, and `[branch "…"]`. Everything else goes: `credential.*`, `include.*` and `includeIf.*`, `core.hooksPath`, `core.fsmonitor`, `core.sshCommand`, `core.editor`, `filter.*`, `diff.*`, `merge.*` drivers, `url.*.insteadOf`. The history, the index and the refs come as they are, so the owner's branch and uncommitted changes arrive intact.

**Empty:** an empty folder with `git init` as `work`, on `main`.

**Starter folders (optional):** "Add Starter Folder…" lets the person pick more Mac folders (style guides, design exports, prompts). Each is assigned to agents in the Team step and copied into those agents' homes in Create (section 5), as each agent. They follow section 7's seed caps, and are never copied into the project.

### 3. The Team step

**Pick a team** (one choice):
- **None:** just you; agents can be added later.
- **A built-in template:** Starter (lead, builder, reviewer) or Orchestrate (lead, builder, reviewer, design keeper), from `core/internal/teamkit/templates`.
- **One of the owner's saved teams** (section 8), by name and when it was saved; **Import Team…** opens a `.portenvteam` file (section 8).

**Adjust**, one row per agent: a checkbox (include or leave out), the avatar, the display name (editable; the id, shown under it in monospace, stays the template's), the role, a runner menu and a model menu (section 9). A row whose runner or model differs from the template's says so ("Changed from Sonnet"). Leaving an agent out also takes it out of the other agents' `message` and `read_clones` lists, which changes those files (they show as changed in Review). Starter folders from the Project step are assigned here, per agent ("Starter files: brand-assets · 38 MB").

**Below the list:**
- "Agents sign in themselves, in their own tabs. Templates never carry logins." (section 10);
- **Start the team when it's ready**, unticked by default. Ticked, Create ends with the team started (it's the team's first-start decision, answered here, section 4); unticked, the team waits for Start in the Team sidebar.

A changed name, runner or model changes the agent file that will be written, and is reviewed as that file (section 4). The template itself is unchanged unless the owner saves it again (section 8).

### 4. Review and approve, inside the flow

**One screen:** a list of the included agents on the left, each with its state (not opened, opened, approved, changed), and the selected agent on the right:
- **The runner line first,** in full and in monospace, as the helper will run it, built from the registry (section 9): `/usr/bin/claude --model opus`. ADR 0022 §3's rule: it's code that will run in the workspace.
- **A change from the template, when there is one,** as its own line above the rest: "Changed from the template: runner claude (sonnet) → claude (opus). Approve Reviewer again." (or "runner claude (opus) → codex (gpt-5-codex)" once other runners exist). Names, message lists and a capped grant (section 7) show the same way.
- **The grants in plain words,** one line each, from the file after section 7's caps: "May open up to 2 panes; you approve each." "May message Lead and Builder." "May ask you." "May request system packages; you approve each." "Reads Builder's copy, never writes it." (`read_clones`). `team_status` is a checkbox here, unticked: "Let Lead see the team's status (states and status lines, never terminals or files)." A routine is listed with its interval.
- **Starter files:** what will be copied into the agent's home, as the agent, with sizes (from the template, from the Project step's folders, and `home_seed` paths from the project, which are checked once the project exists).
- **The instructions,** in full, as plain text (no Markdown rendering, ADR 0022 §6's rule).
- **Where it goes:** `.portenv/agents/reviewer.md` in the project, and the SHA-256 of exactly those bytes.
- **Leave Out** and **Approve Reviewer**, with no default button.

**Approve All** sits under the list. It's enabled only after every included agent has been opened (selected, with its instructions scrolled to the end once); until then it says "Open every agent first (1 left)." It has no default button either, and it approves exactly the files the owner opened: if an agent's file changes after it was opened (a rename in the Team step), it goes back to "changed, not opened".

**The project's summary** heads the list: the source ("github.com/acme/acme-api · main · 2.1 GB, private", "~/Code/acme-api · 812 MB, 9,412 files, dependencies left out", "Empty"), whether it uses LFS or submodules, and the starter folders.

**The first-start decision is part of this screen.** ADR 0022's `team_start` sentence ("Agents work in their own copies. Check their branches before you run them.") is shown once above Create Workspace. With "Start the team when it's ready" ticked, creating the workspace answers `team_start` for the included agents (`start_agent_ids`); unticked, the first Start asks as today.

**Approval pins exactly as ADR 0022 does, only earlier.** The app sends `portenvd` the SHA-256 of each file as shown; `portenvd` writes those bytes, and the pin holds only if what it wrote hashes to that value. A file that differs from what was approved is written but not pinned, and the agent needs approval in the Team sidebar, as any changed file does. A later change (a `git pull`, an agent's proposal) needs approval again, with the diff, as before.

**If the project already has agent files** (found only once it's fetched): the project's file wins for any id the team also has. The template's version is dropped and the project's file is shown in the same review screen before the team is ready; Create pauses there ("acme-api already has an agent called builder. Review its file."). Nothing in the project is overwritten.

### 5. Creating

In this order, each step shown with a check, current or pending marker:
1. **The workspace:** encrypted home, toolbox, start (ADR 0019's `CreateBox`, unchanged).
2. **The project:** cloned, copied or made (section 2), with progress ("Cloning acme-api… 1.2 of 2.1 GB, about 40 s left") and **Cancel**. Cancel stops the transfer, removes the partial project and the new workspace with it (it holds nothing else yet, and no save has been made), and returns to Review with everything as it was.
3. **The agent files:** written as `work` into `<project>/.portenv/agents/`, never over an existing file, and pinned (section 4).
4. **The agents' users and clones:** init makes each agent's user (ADR 0022 §1), and the helper makes each coding agent's clone as that agent (ADR 0022 §2a, #198), here rather than at first start.
5. **The seeds, as each agent** (section 7): the template's seed files and the Project step's folders, streamed from `portenvd` through the existing `WriteHomeFiles` call (which already writes as the agent), then `home_seed` paths from the project. **This fixes today's ignored `home_seed`:** the helper reads `StartTeamAgentRequest.home_seed` and copies it as the agent, with ADR 0022 §2's rules (regular files and folders inside the project, no link leaving it, 100 MB together) and the `~/.portenv-seeded` marker, so a seed is copied once whichever path made it.
6. **Ready:** the window opens with the Team sidebar. Nothing starts unless "Start the team when it's ready" was ticked. The Creating screen says so, and that each agent signs in to its runner in its own tab on its first start (section 10).

**Creation runs nothing a template or project provides:** no runner, routine, hook, package install or script runs during Create. Seed files are written, never executed. Runners start only at Start (or the ticked first start), through the registry (section 9).

### 6. GitHub: one sign-in on the Mac, for clone and push

**The sign-in lives only in the Mac's Keychain.** Never in the workspace, the board, a template, a save, a log or the box's environment. Only the app raises a Keychain prompt (CLAUDE.md); `portenvd` reads the item without UI or fails fast with "Keychain needs your approval. Open Portenv on this Mac to allow it."

**Two ways to sign in,** both in the Project step's Sign In to GitHub…, with Portenv's own as the default:
- **Default: Portenv's own GitHub OAuth app, through the device flow** (the code and github.com/login/device, as the `link-github` board), so testers don't need gh. It's a public client: the app ships only the client ID, and there is no client secret anywhere (a Mac app can't keep one). It asks for the `repo` scope, the least that lets the owner clone and push private repositories. Private repositories work; an organization's SSO or OAuth-app approval is GitHub's own step in the same flow. Registering the OAuth app on GitHub is the owner's step (publishing); until it's done, only the alternative below works.
- **Alternative, if present: the owner's existing gh login, read on the Mac, never copied.** The app reads gh's own Keychain item when a token is needed (it may raise gh's prompt, in front of the person) and hands the token to `portenvd` in memory. Same scope as above.

**The token, stated plainly:** account-wide `repo` scope, read and write on every repository the account can reach, no expiry of its own. It's stored only in the Mac's Keychain (Portenv's sign-in; gh's stays in gh's item). **The sign-in screen says so:** "Portenv can access your repositories; it only uses this for the repository in this workspace." It shows again in the first-push sheet.

**Whichever sign-in, Portenv narrows where the token goes:** one request at a time, to `work`'s git only, for the workspace's own repository (the host and the owner/repo path both checked, with `credential.useHttpPath`), in memory, never stored. Agents can never reach it (`SO_PEERCRED`, below).

**The credential helper, `portenv-git-credential`, for `work` only:**
- **Installed in the image** at `/usr/libexec/portenv/portenv-git-credential` (root's, 0755), and set for `work` by a root-owned, 0644 `/etc/portenv/git-work.conf`, which the image's `/etc/gitconfig` includes only for `work`'s repositories (`[includeIf "gitdir:/home/work/"]`). That file first clears any helper list (`helper =`), then names Portenv's helper for `https://github.com` with `useHttpPath = true`. Neither file is writable by `work`'s ordinary tools or any agent.
- **On each request,** git runs the helper with `get`, and the helper asks the box helper on `/run/portenv/git.sock` (in `/run/portenv-work/`, root:work, 0750). The box helper reads the caller's uid with `SO_PEERCRED` and refuses anything but `work` (uid 1000), then relays over the agent channel (ADR 0010) to `portenvd`, which checks the repository is the workspace's, gets the token from the Keychain and answers. The helper writes the token to git on stdout, the credential protocol's only channel. `store` is ignored (nothing is cached) and `erase` (git's reaction to a refused login) tells `portenvd` to check the sign-in.
- **The clone uses the same path:** in Create, the helper runs the clone as `work` with Portenv's helper, before any agent's user exists, so no agent process is alive while a token is in use.
- **The first push from a workspace asks the owner in the app:** "Allow pushes to acme/acme-api from acme-api?", with Don't Allow and Allow, no default button, and "Pushes come only from your own tabs. Agents can never push." The answer is kept per workspace (on the Mac, with the workspace's config) and can be taken back in the inspector, which shows "This workspace can push to acme/acme-api" with Stop Allowing Pushes. Clone needs no such question: it's what the owner asked for in New workspace.

**Agents can never push:**
- They don't get the helper (the include matches only `/home/work/`), and an agent's uid is refused at the socket by `SO_PEERCRED` even if it runs the binary by hand.
- An agent can't plant a git config that calls the helper as `work`: agents can't write `work`'s repository or home (ADR 0022 §2a, #198), and the helper's settings are root's.
- Their work comes back as a bundle into `work`'s repository (ADR 0022 §2a); the owner looks, merges and pushes. A later "Open a pull request from builder's branch" is an owner action in the app, never the agent's.

**Plain errors** (from the helper on stderr, which git shows in the owner's terminal, and in the app):
- signed out, or a token GitHub no longer accepts (revoked on github.com, or the OAuth app's access removed): "Your GitHub sign-in in Portenv has ended. Sign in again in Portenv, then push again.";
- no push permission on the repository: "Your GitHub account can't push to acme/acme-api. Ask its owner for write access, or push to your fork.";
- a protected branch is GitHub's refusal after sign-in, which git prints; the command block of the failed `git push` (ADR 0020) adds "GitHub refused the push: main is protected. Push a new branch and open a pull request.", recognised from git's output.
- Nothing to type in a terminal for clone or push setup: the sign-in is the app's, the helper is in the image.

**Revocation:** Sign Out of GitHub in the app deletes Portenv's Keychain item (with the gh alternative, Portenv stops reading gh's item and leaves gh's own login alone) and drops any token `portenvd` holds; the next request is refused with the signed-out line. Stop Allowing Pushes refuses that workspace's pushes at once.

**Later (open questions):** a GitHub App for repository-scoped, short-lived access (teams, organizations; ADR 0018's option 1, deferred by R-0326). If it comes: its device flow and PKCE with no client secret anywhere, and, if a refresh needs the secret, refresh only by the owner signing in again, never with a stored secret (R-0324).

**ADR 0018:** its choice (no GitHub App, gh-style access) stands for now; the App is deferred (R-0326). What changes (R-0320, R-0321): New workspace no longer signs the box itself in to GitHub, the login is no longer kept in the box's `hosts.yml`, and agents in the workspace no longer get the owner's GitHub access. A person can still run `gh auth login` in their own tab; that's gh's own choice, not Portenv's, and the inspector says nothing about it.

### 7. Security: an imported template is untrusted input

Relates to ADR 0016 (identity, grants, the owner approves) and ADR 0022 (per-agent users, the content pin, `portenvd`'s validation). Every rule below applies the same to built-in, saved and imported templates, because a saved template sits in a folder on the Mac that other programs can write.

- **The review screen shows exactly what will be written:** the bytes of each agent file (their SHA-256 is what gets pinned) and every seed file's path and size. Nothing in a template is applied that the screen doesn't show.
- **Grants are capped at what a template may ask for,** and a capped grant shows as a change:
  - `panes`: `ask` or `never`; `allow` becomes `ask`;
  - `max_panes`: at most the owner's per-agent limit (ADR 0022 §7: 4 by default, 8 at most);
  - `message`, `read_clones`: only agents included in this team (`read_clones` only those with `checkout: own`);
  - `packages`: `ask` or `never`;
  - `team_status`: `none` unless the owner ticks it for that agent on the review screen;
  - `routine`: within ADR 0022 §4b's limits, listed on review;
  - `share_login`: always removed (it's a no-op today, section 10, and templates never carry logins);
  - `runner: remote` and `via`: refused in templates.
- **No runner path outside the registry:** a template's `runner` must be a registry id (section 9), and its `model` one of that entry's models. A command line in a template (`runner: /tmp/x --flag`, or any argv) refuses the template with a line naming the agent.
- **Seed files are capped and plain:**
  - at most 100 MB and 1,000 files per agent (ADR 0022's `home_seed` limit), 250 MB and 4,000 entries per template, 8 agents;
  - regular files and folders only: never a link, device, socket or FIFO;
  - each path at most 240 bytes and 16 levels, UTF-8 in NFC, no control or bidi character, no `.` or `..` part, no leading `/`, no two paths that differ only by case;
  - written new-only, as the agent, through an `openat` walk with `O_NOFOLLOW`, files 0644 and folders 0755 inside the agent's 0700 home; no execute bit is kept;
  - never a runner's login files (the registry's `login_files`) or files by name that usually hold secrets (`.env*`, `*.pem`, `*.key`, `id_rsa*`, `id_ed25519*`, `.netrc`, `.npmrc`, `.pypirc`, `.git-credentials`, `.aws/`, `.ssh/`, `.gnupg/`, `.config/gh/`, `.docker/config.json`): refused at save and at import, and listed. The list is a seatbelt, not a guarantee; the review screen is.
- **Creation runs no template-provided command** (section 5).
- **Names and labels are plain text:** a template's name 1–40 characters and description up to 200, agents' `name` and `role` per ADR 0022 §3; no control or bidi character, NFC; shown as plain text everywhere.
- **The archive is parsed strictly** (section 8): the manifest first, every other entry listed in it with its size and SHA-256, anything unlisted, duplicated, oversized or of another type refuses the whole import. Nothing is written to `Teams/` until every check passes.
- **GitHub threats** (section 6): a token never in a process's arguments or environment (`/proc/<pid>/cmdline` is readable by every uid, ADR 0022 §1); only `work`'s uid reaches the helper (`SO_PEERCRED`); agents can't plant config that calls it (#198, root-owned settings); the token's account-wide `repo` scope and lack of expiry are stated above and on the sign-in screen, so Portenv's protection is where the token goes (one request, in memory, `work`'s git only, the workspace's host and owner/repo only), not what it can do; there's no client secret to steal (a public OAuth client); the first push per workspace asks the owner; sign-out revokes at once.

### 8. Saved teams

**Save Team as Template…** (the Team sidebar's menu, and Workspace › Save Team as Template…) from any workspace with a team:
- the agent files as last approved (an agent whose file changed since shows "its approved version is saved");
- per agent, optional starter folders (seeds), chosen from the agent's home (through the Agent files view, which reads as the agent) or the Mac;
- a name and a description.
- **Never saved:** logins and secrets (section 7's seed rules), the project's content (unless the owner adds a project folder as a seed, by hand), the board, agents' clones and `share_login`.

**Where:** `~/Library/Application Support/Portenv/Teams/` (a dev lane's app uses its own support folder), 0700, written only by `portenvd`. Each team is a folder named by its id (a ULID), holding one folder per revision (`r1`, `r2`, …); saving again adds a revision, and the newest 10 are kept. The app lists teams by name and newest revision.

**One file to export and import: `<name>.portenvteam`,** an uncompressed POSIX tar (ustar) of one revision, with nothing but:

```
manifest.json            first entry, at most 256 KB
agents/<id>.md           one agent file each (ADR 0022 §3), at most 64 KB
seeds/<id>/<path>        that agent's seed files, copied into /home/<id>/<path>
```

```json
{
  "format": "portenv-team",
  "version": 1,
  "id": "01JAB3Q7Y0ZB0R8M2K5N6P7Q8R",
  "revision": 3,
  "name": "API crew",
  "description": "Lead, builder and reviewer for small services",
  "saved_at": "2026-10-18T21:04:00Z",
  "saved_by_version": "0.1.0+184",
  "agents": [
    {
      "id": "builder",
      "file": { "path": "agents/builder.md", "size": 1184, "sha256": "…" },
      "seeds": [
        { "path": "seeds/builder/docs/style-guide.md", "size": 4096, "sha256": "…" }
      ],
      "seed_bytes": 4096,
      "seed_count": 1
    }
  ],
  "total_bytes": 5280
}
```

- **The runner and model are in the agent file** (section 9), not repeated in the manifest, so there's one source.
- **`version`** is the format's; a reader refuses a version it doesn't know with "This team was saved by a newer Portenv. Update Portenv to open it." **`revision`** counts the owner's saves.
- **Uncompressed on purpose:** sizes in the manifest are the sizes read, so there's no decompression bomb to cap; an archive is at most section 7's 250 MB plus agent files.
- **Import** checks the whole file (section 7), then adds it as a new team (a new id, revision 1) and opens the Team step on it; the review screen is the only way any of it reaches a workspace. Export writes the newest revision through the save panel.

### 9. The runner registry

**One table in code, `core/internal/teamkit/runners.go`, replacing `core/daemon/runners.go`.** `portenvd` builds every runner's argv from it; `StartTeamAgent` passes the helper the argv and the idle-input rule (`idle_pattern` and `idle_lines`) as today. Each entry:

| Field | Claude Code (shown) | Codex (registry only) | grok-build (registry only) |
| --- | --- | --- | --- |
| `id` | `claude` | `codex` | `grok-build` |
| `name` | Claude Code | Codex | grok-build |
| `vendor` | Anthropic | OpenAI | xAI |
| `command` (absolute, in the image) | `/usr/bin/claude` (Anthropic's apt package) | set when it's in the image | set when it's in the image |
| `fixed_args` | none | none | none |
| `model_flag` | `--model` | `--model` | to check |
| `models` (the runner's own aliases) | `opus`, `sonnet`, `fable` | to fill from its help | to fill from its help |
| `idle_input`: `pattern` (RE2, one whole line) and `lines` (how many of the last non-empty lines, from the bottom, 1 to 8) | `❯` or `>` (maybe inside `│` sides) followed by nothing or only the `Try "…"` placeholder, on the last 6 lines | `›` alone, on the last 8 lines | none known |
| `read_only_args` | `--disallowedTools Edit,Write,NotebookEdit` | to check | to check |
| `add_dir_flag` | `--add-dir` | to check | to check |
| `login_hint` | "Claude Code prints its sign-in link in this tab. Sign in once; the login stays in <agent>'s files." | its own | its own |
| `login_files` (home-relative) | `.claude/.credentials.json`, `.claude.json` | `.codex/auth.json` | to check |
| `shown` | true | false | false |

- **The argv** is `command`, then `fixed_args`, then `model_flag` and the model, then `read_only_args` for an agent with `checkout: none` (a lens: belt and braces over the OS boundary, which is the real one), then `add_dir_flag` with each `read_clones` path for a lens. Nothing in an agent file adds arguments.
- **The runner's environment** is the helper's, the same for every runner: the agent's own `HOME`, `USER`, `PATH`, `TMPDIR` and Portenv socket, and `DISABLE_AUTOUPDATER=1`. The image pins each runner's version, so a runner never updates itself or nudges one; for Claude Code that variable turns off its update check and the "Update available! Run: …" notice under its input. The image's login profile sets it for the owner's own shells too. A per-runner `fixed_env` column comes only when a runner needs something the others mustn't get.
- **The command is checked by the helper before start:** a regular file, owned by root, not writable by group or others, under a root-owned path. Never resolved through `PATH`.
- **Routines and the wake on a message** (CC-0498) use `idle_input`: the helper takes the lowest of the last `lines` non-empty lines that the pattern matches in whole; none means the runner isn't at an empty input. A runner may draw a rule and a footer under its input (Claude Code 2.1 does), so the rule names its lines rather than reading the last line only. The screen capture has no cell attributes, so a dim placeholder is told from typed text by its form. An entry without one shows, in the runner menu and on the agent's row: "Routines and wakes are off for grok-build: Portenv doesn't know when it's idle."
- **MVP: the menus list only `shown` entries,** so Anthropic's Claude Code with Opus, Sonnet and Fable. Enabling Codex or grok-build later is filling its entry, adding its package to the image and flipping `shown`; templates, the file format and the review screen need no change.

**The agent file gains `model`** (ADR 0022 §3's keys are amended):

```yaml
runner: claude      # a registry id
model: opus         # one of that entry's models; omitted means the entry's first
```

- **A swap is a different reference** (R-0319): the instructions and grants stay; the file's `runner` and `model` lines change, so the file's SHA-256 changes and it needs approval again (ADR 0022 §3), with the change shown ("runner claude (opus) → codex (…)").
- **Built-in templates ship a default per agent:** Orchestrate and Starter's lead and builder `claude`/`opus`, reviewer and design keeper `claude`/`sonnet`.
- **Hand-written repository files keep today's form** (`runner:` an argv such as `claude --permission-mode acceptEdits`), validated as today, with the first word on the agent's `PATH`. A single word that's a registry id is read as a reference. An argv whose program isn't in the registry keeps today's behaviour: it runs, and routines and wakes are off for it, with the plain line. Templates, saved or imported, accept only references (section 7).
- **The personal override** (ADR 0022 §3) may change `runner` and `model`, by reference only.

### 10. Logins stay manual

- Each runner signs in in the agent's own tab, the first time it starts there; the registry's `login_hint` is the agent's row line until it's done. The login stays in the agent's home and travels with the workspace (ADR 0022 §2).
- **No credentials in templates, ever** (section 7), and Create copies none.
- **`share_login` stays a no-op:** `portenvd` parses it, no decision is raised, and the helper copies nothing. The review screen says "Asks to share your Claude Code sign-in: not available yet, left out." How a runner's login could be offered per runner later is an open question.

### 11. Proto (sketch, appended per the merge guard)

```proto
// daemon.proto, DaemonService
rpc ListRunners(ListRunnersRequest) returns (ListRunnersResponse);          // shown entries, their models and whether routines work
rpc ListTeamTemplates(ListTeamTemplatesRequest) returns (ListTeamTemplatesResponse); // built-in and saved
rpc PlanTeam(PlanTeamRequest) returns (PlanTeamResponse);                    // a template + the owner's adjustments → each file's bytes, SHA-256, caps applied, changes
rpc SaveTeamTemplate(SaveTeamTemplateRequest) returns (SaveTeamTemplateResponse);
rpc ImportTeamTemplate(stream ImportTeamTemplateRequest) returns (ImportTeamTemplateResponse);
rpc ExportTeamTemplate(ExportTeamTemplateRequest) returns (stream FileChunk);
rpc DeleteTeamTemplate(DeleteTeamTemplateRequest) returns (DeleteTeamTemplateResponse);
rpc ListGitHubRepositories(ListGitHubRepositoriesRequest) returns (ListGitHubRepositoriesResponse); // with size and default branch
rpc AnswerPushPermission(AnswerPushPermissionRequest) returns (AnswerPushPermissionResponse);
// CreateBoxRequest gains: ProjectSource project; TeamPlan team (each agent's approved sha256,
// team_status ticks, starter folder assignments); bool start_team. CreateBox streams its steps.

// agent.proto, AgentService (portenvd ↔ helper)
rpc CloneProject(CloneProjectRequest) returns (stream CloneProgress);       // as work; cancellable
rpc CopyProject(stream CopyProjectRequest) returns (CopyProjectResponse);   // the Mac folder, as work
// GitCredential rides TeamCalls' pattern in reverse: the helper asks, portenvd answers;
// the token field is never logged.
```

### 12. The gate (for the code PRs; each step with a mutation that makes it fail, lane E's rule)

- **Team step:** a New workspace with Orchestrate, Reviewer's model changed to Opus and Design left out, writes exactly the three approved files, pinned; Builder's `message` no longer lists `design`; Reviewer's change was shown; a file changed after approval isn't pinned.
- **Imported template:** an archive with a link, an unlisted entry, a size that lies, a `../` path, two paths differing by case, a seed named `.env`, `panes: allow`, `runner: /bin/sh`, or a bidi character in a name is refused or capped exactly as section 7 says, and nothing lands in `Teams/`.
- **Seeds:** copied as the agent (owned by its uid, 0644, no execute bit), never through a link; `home_seed` from the project now arrives (today's bug), once.
- **Creation runs nothing:** a template whose instructions, seeds and project files would write a marker if run leaves no marker.
- **Mac folder:** a folder with an outside link, a FIFO, a set-uid file and a `.git/hooks/post-checkout` arrives without the link, the FIFO, the set-uid bit and the hook, with `.git/config` reduced as section 2 says, and the branch and uncommitted changes intact.
- **Clone:** a repository over the size estimate is refused before any byte; Cancel leaves no workspace and no partial project.
- **Push:** `work` pushes to a test remote through `portenv-git-credential` (a local HTTPS git server and a fake token issuer in the test); the first push waits for the app's answer, the next doesn't. **An agent's push attempt is refused:** running the helper by hand as the agent's uid is refused at the socket, an agent clone's planted `credential.helper` gets nothing, and no token appears in any process's arguments or environment, on disk, in the board or in the logs. Signing out makes the next push fail with the signed-out line.

## Consequences

- **New workspace gets longer and does more:** a tester goes from a GitHub repository to a team ready to Start without a terminal, and every agent file is read and approved before anything runs.
- **`portenv team init` stays** for workspaces that already exist, with the same caps and registry references; the Team sidebar's Add Agent… can offer the templates later.
- **ADR 0018 changes where the login lives, not its choice** (section 6): the GitHub login moves from the box to the Mac's Keychain, and agents lose the owner's GitHub access. No GitHub App for the tester build (R-0326); the App is deferred.
- **ADR 0022 is amended:** the agent file gains `model`; `runner` becomes a registry reference (hand-written argv kept); approval can happen before the file is written; clones and seeds can be made at creation; the helper's ignored `home_seed` is fixed.
- **PLAN 1.7's copy-in moves into New workspace** (1.9); 1.7 keeps the shared folder and the File Provider.
- **New code, after #198's clone code:** `portenvd` (registry, `PlanTeam`, saved teams and the archive reader, the GitHub sign-in and token handling, the extended `CreateBox`), the helper (`CloneProject`, `CopyProject`, the git socket, seeding as the agent, `home_seed`), the image (`portenv-git-credential`, `/etc/portenv/git-work.conf`, the `/etc/gitconfig` include), and the app (the five steps, the review screen, the push question, Save Team as Template…, import and export). Data-loss and security paths: tests first.
- **Open questions** (PLAN): template sharing between owners; signing templates; where seeds live and whether they're encrypted on the Mac; how runner logins could be offered per runner; private Git URLs from other hosts; a GitHub App for repository-scoped access later (R-0326), with R-0324's no-secret rule.
