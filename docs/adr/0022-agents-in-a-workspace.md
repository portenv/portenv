# 0022. Agents in a workspace: a team the owner approves, each agent its own user, one grant check for every way in

Date: 2026-10-16 · Status: Accepted (the reviewer, R-0245), with R-0245's six additions, R-0246 and R-0247 in this text. Per-agent users are the owner's decision (13:19, R-0231; R-0235). `ask-owner --choice` added by CC-0501 (accepted in R-0314). Built from lane A's tmux findings (CC-0345), R-0226, R-0227 and R-0231 to R-0237. Amended 2026-10-18 (the owner's security decision, R-0315): each coding agent works in its own clone and the owner's repository is never agent-writable; see "2a. Amended 2026-10-18 (R-0315)" below, which wins where it differs.

## Context

The owner wants "the agent team" in the tester build (R-0226): agents described in files in the repository, started by `portenvd` in the workspace, each in its own tab, talking to each other and to the owner through a board, with a Team section in the app. Outside agents (Grok Bot, Dots, Muse) must be able to work in a workspace too, through SSH, an MCP connector or web terminals (R-0227).

What's already settled, and what this ADR builds on:
- **ADR 0006:** the owner owns every box; agents are guests; two modes, stand-in (the owner's access) and lane (its own Unix user, `/home/<agent>`, own tmux, own worktree, limits). One core, thin doors, no door bypasses the checks.
- **ADR 0016:** one agent identity (a stable owner-scoped ID and a handle), one pairing flow (`portenv connect`, a device code the owner approves), one credential record per door, one grant model (subject, object, capability, approved_by, state, with an audit trail), one addressed message type (`id`, `from`, `to`, `thread_id`, `type`, `visibility`, `created_at`), an append-only event log, ULIDs.
- **ADR 0010:** `portenvd` reaches the box only through the box agent ("the helper") over pinned TLS on the Mac's loopback; never `docker exec`, never the engine's socket in a box.
- **ADR 0020:** each tab is its own terminal; every command carries an author set by `portenvd` from the caller's identity, never from the request (`CommandAuthor`, `author_agent_id`); `caller` in `core/daemon/caller.go` already has an agent kind waiting for this ADR.
- **ADR 0015:** public doors only on the user's own server, opt-in per server, off by default; the Mac never opens an inbound port.

Facts about today's workspace that decide the design:
- Everything the person runs in a box runs as `work` (uid 1000), and `work` has `NOPASSWD: ALL` sudo (`images/toolbox-node/rootfs/etc/sudoers.d/work`).
- The workspace's tmux server runs as `work`, and its socket is reachable by any `work` process.
- **Lane A's findings (CC-0345):** an agent running as `work` can, with plain `tmux`: make tabs the app shows, split the owner's tab, read the owner's screen and history (`capture-pane`), type into the owner's shell (`send-keys`), and turn the helper's passthrough seal back off. Hiding `TMUX` or the binary is no boundary.
- So **nothing inside the box can hold an agent that runs as `work`**: it is the owner, with root. Grants would be advice.

## Decision

### 1. Each agent is a user of the workspace (the lane model of ADR 0006, now)

**Decided by the owner (R-0231, 13:19, replacing R-0228; R-0235, 13:28):** an agent is a user of the workspace, required in the first tester build. It has a Unix login, a private home, its own processes and its own tmux server. **The isolation is the operating system's; the `portenv` command and its grants sit on top.** There is no "agents run as you" build.

**The owner** is `work`. Only the owner creates and removes agents, changes grants, sees agents' homes and answers decisions, all through the app. `work` keeps its sudo: the owner can also read any agent's home from a terminal. No agent can do any of these things.

**The users.**
- **The login name is the agent's id** (`builder`; section 3's rule). The uid and gid come from 2001–2008, inside ADR 0010's lane range (2000–2999); eight is the team ceiling (section 7).
- **Each user has:**
  - home `/home/<id>`, 0700, owned by the agent;
  - `/bin/bash`, a locked password, and no `sudoers` entry;
  - its own group, plus the group `team` (gid 2000, which `work` is also in).
- **Ids that can't be agents:** any name already in the image's `/etc/passwd` or `/etc/group` (`root`, `work`, `portenv-agent` and the rest), `team`, and anything starting with `portenv`. `portenvd` refuses them with a plain line.
- **Only init makes or removes users.** init (`portenv-agent init`, root, PID 1) already serves the agent API on `/run/portenv/agent.sock`. Only the helper's API process can reach that socket (root:991, 0710). init gains two calls there, `EnsureAgentUser(id)` and `RemoveAgentUser(id)`:
  - init checks the id itself; it doesn't rely on the helper's check;
  - it takes the lowest free uid in the range;
  - it writes `/etc/passwd`, `/etc/group` and `/etc/shadow` atomically (a new file, fsync, rename, under one lock);
  - it makes the home;
  - it records the user in `/home/.portenv/team/users.json` (root's, 0644).

  Nothing else in the box writes those files. The helper's API process gains no capability: it asks.
- **At every box start,** init reads `users.json` and writes the lines again before the helper starts. An agent therefore keeps the same uid (its files' owner) across a save, a restore and a Move To.
- **Removing an agent never deletes its files on its own:**
  - `RemoveAgentUser` takes the user's lines out. The home and the uid stay, marked removed in `users.json`.
  - They stay until the owner chooses "Remove and delete its files", a decision that shows the size. Only then does init delete the home and free the uid.
  - This is a data-loss path: tests come first.
- **Names:** the app and every message show the agent's display name ("Builder").

**The helper's user switch.**
- **Sockets.** init makes `/run/portenv-team/` (root, 0711). For each agent, init (root) also makes `/run/portenv-team/<id>/`, owned by the helper (991) with the agent's group, setgid, 0710, so only that agent and the helper can pass through and only the helper writes in it. It does this in `EnsureAgentUser`, again at every start (`/run` is lost when the box stops), opening each folder without following a link, and returns the paths. The helper has no `cap_dac_override` and makes no folders there, and gives nothing to another uid. Inside it:
  - `portenv.sock`: the agent's `portenv` socket (section 4), 991 with the agent's group, 0660. The helper makes it by listening; the folder's setgid bit gives it the agent's group, which the helper checks before serving;
  - `tmux/`: the agent's own, 0700, for its tmux server's socket;
  - `tmp/`: the agent's own, 0700, its `TMPDIR` (below).
- **Processes.** The helper's API process (uid 991) already holds `cap_setuid`, `cap_setgid`, `cap_chown` and `cap_kill` (ADR 0010), and needs no new capability. For each agent it starts two processes as the agent's user. Each runs in a forked child that calls `setgroups([its group, team])`, `setgid` and `setuid`, clears the ambient capability set and sets `no_new_privs` before `exec`. So nothing an agent runs can gain a capability or use a set-uid binary, `sudo` included. The two processes are:
  - the agent's own tmux server, on `/run/portenv-team/<id>/tmux/default`;
  - one control client (ADR 0020's control mode) attached to that server, which talks to the helper over pipes. It runs as the agent because tmux accepts only clients with the server's own uid.
- **The team's first start** waits for the owner's `team_start` decision. The decision names the agents the owner chose Start on (`start_agent_ids`, added as they click), and on Approve `portenvd` starts them itself, from the board, not from the app's memory, so it works after the app or `portenvd` restarts (R-0296). Each shows `starting` at once; one that can't start goes back to `stopped`, with the reason ("Couldn't start: …") as its row's line and under Start until a start works, and as a board notice in its column (the notice's `about` names the agent, R-0302).
- **Stop:** the helper ends the agent's tmux server, then kills every process with the agent's uid (`cap_kill`).
- **`work`:** its tmux server and control client stay as they are, on `work`'s own 0700 socket directory.

**What this guarantees (the boundary; it is never what gets cut for time):**
- **Raw `tmux` from an agent reaches only its own server.** It can't open `work`'s socket or another agent's (directory modes), and tmux refuses other uids anyway.
- **`portenv split` and `portenv tab new` (section 4) are the only way into the owner's window,** and only after `portenvd`'s grant check. Those panes live on the agent's own server; the app shows them in the owner's window, labelled with the agent.
- **No agent reads, splits or types into the owner's terminals or another agent's.** There is no command for it, and no socket to do it with.

This closes every item of lane A's list:

  | Lane A's finding | With per-agent users |
  | --- | --- |
  | `new-window` makes a tab the app shows | Only in the agent's own server; the app shows it under that agent, labelled |
  | `split-window -t <owner tab>` | No socket to reach the owner's server |
  | `capture-pane` reads the owner's screen | Same |
  | `send-keys` into the owner's shell | Same; and `portenv` refuses keys to any pane the agent didn't open |
  | `allow-passthrough on` undoes the seal | Only on the agent's own server, whose control client the helper restarts sealed |

- **Separately (lane A's small fix, which goes in regardless):** each tab streams the pane recorded when it was made, never "the active pane", so a split can't swap what a tab shows.

**What agents still share with the owner, stated (R-0245):**
- **`/tmp` and `/dev/shm`** are one per box, shared by every user.
  - Each agent gets `TMPDIR=/run/portenv-team/<id>/tmp`, its own, 0700, so tools that honour `TMPDIR` keep their files out of `/tmp`.
  - At every team start, the helper checks that `work`'s known sockets can't be reached by other uids, and refuses to start the team with a plain line if one can. The sockets checked: `SSH_AUTH_SOCK`'s, a gpg agent's, a VS Code server's, tmux's, and anything of `work`'s under `/tmp`.
  - Lane E's rows (section 12) prove that no agent reaches them.
- **`/proc`:** any user in the box can read every process's command line (`ps`), the owner's included. Hiding it (`hidepid=2`) needs `/proc` mounted again with that option, which needs `CAP_SYS_ADMIN`; the box doesn't have it, and the engine mounts `/proc` itself. **Residual risk:** a secret passed as an argument is readable by agents. Other users' environments (`/proc/<pid>/environ`) stay owner-only. `portenv guide` tells agents never to put secrets on a command line, and Portenv's own commands never take one there.
- **The network:** agents share the box's network.
  - They can reach anything listening in the box, the owner's dev servers and databases on `localhost` included, and whatever the box itself can reach outside.
  - The agent channel stays safe: it is pinned TLS with keys only `portenvd` and the helper hold (ADR 0010), so an agent that reaches its port gets nothing.
  - init's socket, `portenvd`'s and the helper's per-agent sockets are protected by file modes, not by the network.
  - Per-agent network namespaces are a later option.

### 2. Files, secrets and identity for per-agent users

**Who reaches what:**

| Where | Who | Modes |
| --- | --- | --- |
| `/home/<id>`, the agent's home | that agent; the owner through the app | 0700, the agent's |
| `/home/team`, the team folder | every agent and the owner | `work:team`, 2770, default ACL `g:team:rwX` |
| the project, `/home/work/<workspace>`, `.git` included | the owner writes; agents read | `work`'s; agents get `g:team:rX`, never write (section 2a) |
| an agent's clone, `/home/<id>/<workspace>` | that agent writes; the lenses the owner approves read it (section 2a) | in the agent's 0700 home |
| the rest of `/home/work` | the owner | `/home/work` 0711; dotfiles and logins 0700 |

**Agents' homes are workspace state (R-0234).**
- They live in `/home`, so they are encrypted with the workspace, saved in its backups and moved with it (Mac, server, hosted). They are never in the repository.
- **Seeding:** an agent file may list `home_seed: [paths in the repository]`. On the agent's first start, as the agent's user, the helper copies them into its home at the same relative paths. The rules:
  - only regular files and folders inside the project, with no symlink leaving it, at most 100 MB together;
  - a marker, `~/.portenv-seeded`, records the copy; later changes in the repository are not copied again.
  - **As built (R-0323):** the helper runs `team-io seed-home` as the agent, with the team group so it reads the project as the agent can, before the agent's terminal starts. It walks through descriptors and never follows a link. Links, special files and hidden names (`.bashrc`, `.claude/…`) are skipped and reported, not copied. A missing path is skipped. It writes only new files (0600, or 0700 when executable), so a file the agent already has is kept. The copy is at most 100 MB and 10,000 files together, within the home's quota; past either, nothing is copied, no marker is written and the start fails with the reason. It reads the owner's project, so it works the same once agents have their own clones (§2a).
- **The Agent files view:** the owner lists, opens (a copy to the Mac) and adds files (drag and drop) for each agent in the app. The helper does each read and write as the agent's user, in a forked process with its uid, so new files belong to the agent. Nothing else crosses: no agent reads another's home, and no agent call reaches this view.
- **Quota:**
  - each agent's home has a limit (2 GB by default, 20 GB at most, section 7), and it counts toward the workspace's size;
  - the box has no kernel quotas today (they need `CAP_SYS_ADMIN`, which it lacks), so the helper measures each home at start, every five minutes and on every add from the app;
  - at 90% the sidebar says "builder's files are almost full (1.8 GB of 2 GB).";
  - at 100% it says "builder's files are full (2 GB of 2 GB). Remove some in Agent files, or raise its limit in Settings." The app then refuses adds, and the agent's `portenv` calls answer with the same line;
  - the kernel doesn't stop the agent's own writes past the limit. That waits for kernel quotas (open questions; the rootless spike, R-0223).
- **Google Drive connectors** come after the tester build, since they need grants and a sign-in. Until then the owner exports the files and adds them in the app.

**The team folder, `/home/team`:** files every agent shares. init makes it, and it persists like the homes. It has the group `team`, setgid, and a default ACL, so files stay group-writable whatever the umask. The helper never runs anything from it.

**How agents change the project (the coordinator's question 1). Chosen (amended 2026-10-18, R-0315): each coding agent works in its own clone; the owner's checkout and its `.git` are never agent-writable.** Section 2a has the decision and how it's done. What follows was the first design, and stays here so the change is clear:
- ~~Everyone works in the owner's checkout, with the project's group `team`, setgid directories and default ACLs `g:team:rwX`, and `core.sharedRepository = group` in the owner's `.git/config`.~~ **Superseded:** a shared, writable `.git` lets agents influence what the owner's git runs. Protecting `.git/hooks/` and `.git/config` alone doesn't close that: git reads more than those two paths for the person who runs it, and the system `safe.directory` entries that let agents use `work`'s repository (and the worktrees under it) apply to `work` too.
- **Still true:** the project's `.portenv/` (agent files, section 3) is `work`'s, and agents can't change it.
- **Corrected (this line said agents' config writes fail, which was true only of `.git/config` itself):** agents don't write to the owner's repository at all. An agent's git commands, config writes included (`git remote add`, `branch --set-upstream-to`), work in its own clone and change nothing the owner's git reads.
- **The owner's home:** `/home/work` becomes 0711 (agents can pass through, not list). The owner's dotfiles and logins (`~/.config`, `~/.claude`, `~/.ssh`) stay 0700. The helper refuses to start a team over a home whose modes would expose them, with a plain line.
- **The residual risk, stated plainly:** an agent's work reaches the owner only as a branch the owner fetches (section 2a). Once the owner checks out or merges that branch and runs its `Makefile`, a test or a `package.json` script, it runs as `work`, with sudo. That is the same trust as merging an agent's pull request without reading it, and the app says so in the decision the owner approves when a team first starts (`team_start`, R-0245), and again in the Team section's header ("Agents work in their own copies. Check their branches before you run them."). Files in `/home/team` are shared too, and the same holds for any of them the owner runs. git shows every change on an agent's branch.

**How it's done (`PrepareTeamProject`, before each agent start; amended by section 2a):**
- init (root) only checks: real folders, the project is `/home/work/<name>`, and the owner's private folders and logins aren't readable by others (else the team is refused with a line naming the one to fix).
- The project, `.git` included, is `work`'s, readable by `team` and writable by no agent (section 2a's checks, at every team start).
- ~~init adds the project as a system git `safe.directory`, so agents can use `work`'s repository.~~ **Removed (R-0315):** Portenv adds no system `safe.directory` entry, and removes the ones it added before (section 2a, migration).
- **Interim, until the clone code lands (main today):** the walk runs through descriptors and changes only what `work` owns (#196, CC-0494); `.git/config` and `.git/hooks` stay readable by `team`, never writable, and `work`'s worktrees inside the project are trusted by name (#197, CC-0493). Section 2a replaces both: `.git` goes back to `work` alone and those `safe.directory` entries are removed.
- It happens only when a team starts, never in a workspace without one. Stop Team doesn't revert it, and the team-start decision says so (R-0261).

**Rejected: a bind mount of the project into each agent's home.** The uids still differ, so a plain bind mount changes no permission: the agent needs group write anyway. Making an agent's writes land as `work` needs an idmapped mount, which needs `CAP_SYS_ADMIN` in the box. The box doesn't have it, and this ADR won't add it. It would also make an agent's writes look like the owner's.

**~~Considered, a later option~~ Adopted (R-0315): each agent in its own clone.** The first text offered it as a later agent-file option (`checkout: own`). It is now how every coding agent works, made as a full local clone rather than `git clone --shared` (section 2a says why).

**The owner's secrets (question 2):** an agent doesn't have `work`'s home, so it has none of the owner's logins: Claude Code's sign-in, git credentials, SSH keys, cloud tokens. It also has no sudo: a system package it needs is a package decision for the owner (section 6), which Portenv installs on Approve. User-level installs need no decision: `npm`, `pip`, `uv` and the like, in the agent's clone or elsewhere in its own home. That's the point, and it has a cost:
- **Each agent signs in once,** in its own tab (Claude Code prints its sign-in link there). The login stays in that agent's home and travels with the workspace like everything else in `/home`.
- **Or the owner shares one login, explicitly, per agent:** an agent file may ask `share_login: [claude]`. `portenvd` asks the owner once ("Let builder use your Claude Code sign-in?"), and the helper copies only that tool's credential file into the agent's home, 0600. The prompt says plainly that a shared login can't be taken back by removing the agent: the owner signs out of that login to end it.
  - **Not in the tester build (R-0323):** `share_login` is parsed and checked but does nothing: `portenvd` asks no decision and the helper copies no credential. So the file never looks obeyed, the agent's column says so ("Sharing your sign-in (claude) isn't in this build, so nothing is shared. Sign Builder in from its own terminal if it needs to.", `AgentInfo.share_login_off`), and so do the agent's `portenv whoami` (`WhoAmIResult.share_login_off`) and the agents' guide (`portenv guide`'s rules, R-0324). Whether and how to build it is an Open question in PLAN.md.
- **git pushes:** agents don't get the owner's git credentials. They commit on `agent/<id>` in their own clone (section 2a); a push to the remote is the owner's, after fetching the branch (or the lead's, from its own clone, if the owner grants `push` and shares a login for it).

**Identity (question 3):** see section 5. Every way in, local or remote, lands on the same per-agent user.

### 2a. Amended 2026-10-18 (R-0315): each coding agent works in its own clone

**The owner's security decision (2026-10-18, relayed in R-0315).** A shared, writable `.git` lets agents influence what the owner's git runs. File modes on a few paths in `.git` don't close that, and a system `safe.directory` that lets agents use `work`'s repository widens it, because `work`'s git reads the same system config. So the boundary moves: agents never write the owner's repository, and their work comes back as branches. This supersedes R-0280's worktrees for agents under the owner's project, and the interim system `safe.directory` entries for them (PR #197) go away.

**1. `work`'s repository is never agent-writable.**
- The project folder, its `.git`, and everything git reads for `work` there stay `work`'s alone: owned by `work`, with no group or ACL write for `team` or any agent, and no default ACL that would give one.
- Agents may read the project (`g:team:rX` on the tree, which they need to clone and to fetch the owner's new commits). Read is all.
- No `core.sharedRepository`, and no system (`/etc/gitconfig`) `safe.directory` entry from Portenv, for the project or anything under it. git's own ownership check then refuses, for `work`, any repository under the project that an agent made.
- **Checked at every team start, before any agent process runs:** the helper walks the project as `work`, through descriptors, never following a link. Anything under `.git` that isn't `work`'s, or is writable by another uid, stops the team's start with a plain line naming it; the migration below is the only thing that fixes it for the owner.

**2. Each coding agent has its own clone.**
- **Where: `/home/<id>/<workspace>`,** in the agent's own 0700 home, owned by the agent. Chosen over a per-agent folder inside the project because nothing agent-writable then sits in the owner's tree, where the owner's git, editors and build tools walk; and the clone travels, saves and is measured with the rest of the agent's home (section 2's quota).
- **How: a full local clone, made by the helper as the agent's user** (a forked process with the agent's uid, groups, no capabilities and `no_new_privs`, as section 1's user switch), at the agent's first start:
  ```sh
  git -c safe.directory=/home/work/<workspace> clone --no-hardlinks --origin owner \
      /home/work/<workspace> /home/<id>/<workspace>
  git -C /home/<id>/<workspace> switch -c agent/<id>   # from the owner's current branch, or the existing agent/<id> if the owner's repository has one
  ```
  The `safe.directory` is given for that command only, and written once into the agent's own `~/.gitconfig` for its later `git fetch owner`; only that agent's git reads either. Portenv never writes one into the system config, which `work`'s git also reads.
- **Not `git clone --shared`,** which the first text suggested: a shared clone borrows the owner's object store, so the owner's `git gc` can remove objects the agent's branch still needs, and the clone stays tied to reading the owner's `.git` for its whole life. A full clone costs disk (it counts toward the agent's home limit, section 7), and stands alone. If the project is larger than the agent's free space, the start is refused with a plain line ("builder's files can't hold a copy of the project (3.1 GB; its limit is 2 GB). Raise its limit in Settings.").
- **Its branch is `agent/<id>`.** The clone's remote `owner` is the owner's project, readable and never writable, so `git fetch owner` brings the owner's new commits and a push to it fails. Agents have no credentials for the project's real remote (section 2), so nothing else receives their pushes either.
- **What a clone doesn't have:** the owner's untracked files (dependencies, build output, local settings such as `.env`). The agent installs its own dependencies in its clone. Files the owner wants it to have go through `home_seed` or the Agent files view (section 2).
- **Agent file:** `checkout: own` (the default, and the only choice for an agent that writes code) or `checkout: none` (a lens, point 4, or an agent that doesn't need one). A `checkout: none` agent can't run `portenv share`.

**3. Hand-back is by branch, and `work`'s git never opens an agent's repository.**
- An agent says its work is ready with `portenv share [agent/<id>]` (section 4). The owner chooses Fetch in the app (or Portenv fetches on `share`, open question), and the branch appears in the owner's repository as `refs/remotes/agents/<id>`. The owner looks at it, merges it, or deletes it, with ordinary git.
- **The fetch, exactly.** It has two halves, each run by the helper in its own forked process, with no terminal (stdin `/dev/null`), a fixed environment and no shell:
  1. **As the agent's user, in the agent's clone:** `git -C /home/<id>/<workspace> bundle create - refs/heads/agent/<id> --not --remotes=owner`, streamed to the helper (without the `--not` part if the owner's repository lacks a prerequisite), at most the agent's home limit in size. Whatever the agent's clone's config does here, it does as the agent.
  2. **As `work`, in the owner's project:** the helper writes the bundle to `/home/work/.portenv/team/handoff/<id>.bundle` (`work`'s, 0600, in a 0700 folder), then runs
     ```sh
     git -C /home/work/<workspace> \
         -c protocol.allow=never -c protocol.file.allow=always \
         -c core.hooksPath=/dev/null -c core.fsmonitor=false \
         -c transfer.fsckObjects=true -c fetch.fsckObjects=true \
         fetch --no-tags --no-recurse-submodules --no-write-fetch-head \
         /home/work/.portenv/team/handoff/<id>.bundle \
         +refs/heads/agent/<id>:refs/remotes/agents/<id>
     ```
     and deletes the bundle. A bundle is data: refs and a pack, checked object by object. `work`'s git reads only its own repository's config and that file, never the agent's config, hooks or metadata; the overrides make the one command allow only a local file, run no hook or monitor, and refuse a malformed object.
- **Rejected: fetching straight from the agent's clone** (`git fetch /home/<id>/<workspace>` with hooks and config overridden on the command line, and `file://` allowed only for that path). The process that reads the agent's repository still runs as `work` and still parses the agent's config; command-line overrides cover only the settings someone thought of. git's ownership check refuses that repository for `work` by default, and Portenv never adds the `safe.directory` that would let it through.
- **The owner never needs to run git inside an agent's clone,** and Portenv never makes that possible: no system `safe.directory` for agents' homes, and `work`'s git refuses them by ownership. Reading an agent's files (the Agent files view, or `sudo` from a terminal) is reading, not running its config.

**4. Read-only lenses (reviewer, design, trust-safety) read clones and never write.**
- A lens's agent file names the clones it reads: `grant.read_clones: [builder, lead]` (only team agents with `checkout: own`; an override can only narrow it). The owner approves it with the rest of the file.
- **The grant:** the helper gives the lens a named-user ACL on the writer's clone, `u:<lens>:rX` with the matching default ACL, so new files stay readable to it, and `u:<lens>:x` on the writer's home, so it can pass through to the clone and list nothing else there. Never `w`, and never through the group `team`, which every agent is in. It's reapplied at every team start and taken away when the grant goes.
- **A lens reads files; it doesn't run git in another agent's clone.** git refuses it there by ownership, Portenv never adds a `safe.directory` for it, and `portenv guide` tells lenses not to add one themselves: that would let the writer's config run as the lens. To read a writer's history, a lens has its own clone (`checkout: own` with nothing to share) and fetches the writer's branch with the same two-half fetch as point 3, into `refs/remotes/agents/<id>` (open question: whether lenses need this in the first build).

**5. The gate (`make team-start`, for the code PR; each step with a mutation that makes it fail, lane E's rule):**
- `work` makes a commit in the project; builder starts and has `/home/builder/<workspace>` on `agent/builder`, its own, 0700 home, with remote `owner`.
- Builder can't create, change or remove anything under the project's `.git`, or anything in the project's tree.
- **Builder tampers with its own clone's config:** it sets, in its clone, settings that would run a command (writing a marker file) if git honoured them for whoever runs git there. Builder commits on `agent/builder` and runs `portenv share`.
- `work` then runs `git status`, `git log`, `git diff` and a commit in the project, and the owner's Fetch brings `refs/remotes/agents/builder` with builder's commit. **The marker never appears, and no process of `work`'s uid ran one of builder's commands.**
- `work`'s git run inside builder's clone is refused by ownership, and the system git config has no `safe.directory` from Portenv.
- A lens with `read_clones: [builder]` reads builder's files, can't write any, can't list builder's home, and its git in builder's clone is refused; a lens without the grant can't read them.
- The migration below, from a workspace set up the old way, ends with every check above passing and nothing of the agents' lost.

**6. Migration: a workspace whose team used the shared project.** It runs at the next team start, before any agent process runs; it's a data-loss path, so its tests come first.
- **Stop first:** the helper checks that no process of an agent's uid is running (Stop kills them all, section 8), so nothing an agent runs can race what follows.
- **Record what git reads for `work`, before anything changes:**
  - every config file, hook and worktree registration under `.git` that isn't `work`'s, or that another uid could write;
  - every repository or worktree under the project that an agent made or worked in: a `.git` folder or file that isn't `work`'s, and the worktrees made for agents under R-0280, whoever owns them.
- **Each agent gets its clone,** made as in point 2 from the owner's repository, on its existing branch if it had one (its commits are already in the owner's repository). Its old worktree's uncommitted changes are copied, as the agent, into the new clone (not committed). Nothing is deleted.
- **`.git` back to `work` alone.** init (root, which alone can change an owner) walks `.git` through descriptors, never following a link: everything becomes `work`'s, the `team` ACL entries and default ACLs go, and the modes lose group write. `core.sharedRepository` comes out of the owner's config, and Portenv's own `safe.directory` entries come out of the system config. A link inside `.git` that `work` didn't make stops the migration with a line naming it, for the owner to look at.
- **The rest of the tree back to read-only for agents:** the same walk makes the project's agent-owned files `work`'s, with ACLs `g:team:rX`, except the repositories and worktrees recorded above, which it leaves as they are (git refuses them for `work` by ownership until the owner decides).
- **The owner decides on the record before the team starts again:** the team-start decision lists it ("Agents could have changed these: …"). For each entry the owner keeps it (it becomes `work`'s, and the owner takes its content as their own) or removes it; an old worktree's removal shows its size and says its uncommitted changes are already in the agent's clone. Nothing is deleted without that answer, and the team doesn't start until the owner gives it.
- The team-start decision says once that the team now works in its own copies, and that agents' changes come back as branches.

### 3. Agent files

**Where:** `<project>/.portenv/agents/<id>.md` in the repository, so a team travels with the code. The project is the workspace's project folder (`/home/work/<workspace name>`, as first run makes it). A workspace without one has no team, and the Team section says so.

**Personal overrides:** `~/Library/Application Support/Portenv/agents.local/<workspace id>/<id>.md` on the Mac, never in the repository or the box. An override may change `runner`, `avatar`, `share_login` and narrow a grant. It can't widen a grant past the repository file or the owner's limits, or add `home_seed`.

**Format:** YAML frontmatter, then Markdown instructions (the agent's system prompt or `AGENTS.md`, handed to it at start as `AGENTS.md` in its home).

```yaml
---
id: builder                 # [a-z][a-z0-9-]{0,31}; the file's name must match
name: Builder               # 1–40 characters, shown in the sidebar
role: Writes the code the lead asks for   # up to 120 characters
runner: claude --permission-mode acceptEdits   # an argv, split like a shell would but run with no shell; or "remote"
via: mcp                    # only with runner: remote — ssh | mcp | web
avatar: hammer              # an SF Symbol name, or a colour from GUIDELINES §11's agent colours
share_login: []             # tools whose sign-in the owner may share (section 2); asked, never assumed
home_seed: [docs/style-guide.md]   # repository paths copied into its home on first start (section 2)
checkout: own               # own | none — its own clone on agent/<id> (section 2a); default own
grant:
  panes: ask                # ask | allow | never — may it open panes and tabs
  max_panes: 4              # 1–8
  message: [lead, reviewer] # agent ids it may message; the owner is always allowed
  ask_owner: true           # may it ask the owner (portenv ask-owner)
  packages: ask             # ask | never — may it request system packages (section 6); default ask
  read_clones: []           # agent ids whose clones it may read, never write (section 2a, lenses)
---
You build what the lead asks for. …
```

**Validation, by `portenvd`, before anything starts:**
- only the keys above, with the types and ranges shown; the file at most 64 KB, its instructions at most 32 KB, UTF-8 without control or bidi characters (ADR 0020's rule) in `id`, `name` and `role`;
- a local `runner`'s first word must be on the agent user's `PATH` in the box (the helper checks; `portenvd` never runs it to find out);
- `message` and `read_clones` name only agents in the team (`read_clones` only ones with `checkout: own`); `max_panes` within the owner's limit (section 7);
- `id` isn't a reserved name (section 1); `home_seed` paths are inside the project (section 2);
- each problem is one plain line the app shows by the agent's row ("builder: runner `claude` isn't installed in this workspace. Install it, then choose Start.").

**Approve shows the `runner` line first,** in full and in monospace, above the diff: it is code that will run in the workspace (R-0245).

**Two locks on the files:**
- **The owner approves each file's content before it runs.** A file is pinned by its SHA-256 once the owner approves it in the app. A changed file (an agent's proposal, a `git pull`, a teammate's commit) stops nothing that's running, but won't start again until the owner approves the new content, with the diff. This matters because a repository is shared: a commit from someone else could add an agent with `panes: allow` and any `runner`, which is code that runs in the owner's workspace.
- **Agents can't edit them in place.** The project's `.portenv/` is `work`'s and never group-writable (section 2), so an agent's uid can't change an agent file. It proposes a change with `portenv propose-agent-change <file>`, which lands as a decision for the owner, with the diff, and then only runs once the owner approves the new content.

**Templates:** `portenv team init` offers three, and asks the owner to approve each file it writes:
- **Starter:** three agents for Claude Code (lead, builder, reviewer).
- **Orchestrate team** (R-0232, rebuilt by R-0340): how this project is run, as a template. A coordinator (assigns lanes, runs the merge chain, keeps the tracker, `team_status: read`, a 30-minute status round that reports only blockers), a reviewer on a different model that can't edit files and gives numbered decisions, three parallel builder lanes (`builder-a` to `builder-c`, each with its own clone), and DevOps; a design keeper ships off. Its rules: one task per lane, a test that failed before the change in every PR, the 200-deleted-line guard only the owner waives, no force-push, hand merges with a remerge-diff and a tree check, numbered decisions on the board, and owner approvals only in the app. Until R-0340 it was lead, builder, reviewer and design keeper; the owner's run sheet (section 11) uses the new team.
- **App product team** (`app-product`, R-0338): an engineering lead (`team_status: read`, a 30-minute status round that reports only blockers), a code writer, a code reviewer that can't edit files, a product designer, a design-system keeper, trust and safety and support (no clone, can't edit files), an App Store release manager and DevOps (the project's runbook first, production only by an approved decision). A social media manager ships off (drafts only, never publishes).

**Agents a template ships off** are written to `.portenv/agents/off/<id>.md`. `portenvd` and the helper read only `<id>.md` directly in `.portenv/agents/`, so a file in `off/` is never loaded, approved or started; the owner turns one on by moving it up a folder and adding it to its teammates' `grant.message`, then approves the changed files. This is a folder convention of `team init`, not a new key in the agent file. Templates don't set `grant.read_clones` until portenvd parses it; the reviewer's file names it in a comment, and the writer hands the reviewer its diff (`portenv handoff`) meanwhile.

### 4. `portenv` inside the box

**The commands** (each runs as the calling agent's identity, section 5, through the same grant check as every other way in):

| Command | Grant | Notes |
| --- | --- | --- |
| `portenv split [--right\|--down] -- <cmd>` | `panes` | splits the agent's own current pane; counts toward `max_panes` |
| `portenv tab new --name <n> -- <cmd>` | `panes` | a new tab in the agent's own server, shown under it in the sidebar |
| `portenv focus <pane>` | own panes only | |
| `portenv close <pane>` | own panes only | |
| `portenv msg <agent> "<text>"` | `message` | at most 8 KB; to the board (section 6) |
| `portenv ask-owner "<text>" [--choice "<label>" …]` | `ask_owner` | a decision for the owner (section 6), which the agent waits for or polls; with 2 to 4 `--choice` labels the owner picks one (CC-0501) |
| `portenv request-package <name> [--reason "<text>"]` | `packages` | a package decision for the owner (section 6); the result comes to the agent's inbox |
| `portenv inbox [--wait] [--since <cursor>]` | always, own messages only | JSON lines |
| `portenv share [<branch>]` | `checkout: own` | a message to the owner and the lead that its branch (`agent/<id>` in its own clone) is ready for the owner to fetch (section 2a) |
| `portenv propose-agent-change <file>` | always | a decision for the owner, with the diff |

- **Never:** keys into a pane the agent didn't open; anything in the owner's panes; another workspace; anything about keys, saves, Move To or the workspace's lifecycle.
- `panes: ask` makes each `split` or `tab new` a decision for the owner ("builder wants a new tab running `npm test`"); `allow` runs it at once; `never` refuses it with a plain line.
- The CLI is the same `portenv` binary, already in the image; inside a box it talks only to the helper's per-agent socket (below), never to `portenvd`'s socket on the Mac (which isn't there).

**The path:** the in-box command → the helper's socket `/run/portenv-team/<id>/portenv.sock` (section 1's modes) → the helper fills in the caller from the socket (below) → over the existing agent channel to `portenvd` → the grant check → the action, carried out by the helper. The helper has no say in grants; it only names the caller.

### 4a. What an agent learns about itself (R-0246, R-0247)

Agents learn what they can do from Portenv, not from guesses. Everything below comes from what `portenvd` enforces, so it can't drift from it.
- **`portenv whoami --json`** reports:
  - the agent's id, name and role;
  - the workspace and where it runs (Mac, server or hosted);
  - the effective grant after overrides: panes (with `max_panes` and how many are open), message targets, `ask_owner`, `packages`;
  - its limits and current use: home size and quota, pending decisions, package requests today;
  - its teammates (ids, names, roles, states);
  - the commands it may use now, and the runners on its `PATH` that pass section 3's check.
- **A generated block at the top of `AGENTS.md`,** written by `portenvd` at every start from the grant, in plain text with no secrets:
  > You are Builder in Portenv. You can: open up to 4 panes (the owner approves each), message lead and reviewer, ask the owner (`portenv ask-owner`), request system packages (`portenv request-package`). You can't: reach the owner's terminals or other agents' files. Run `portenv guide` for the board, and `portenv whoami` for what you can do now.
  The agent file's instructions follow it.
- **Grant changes reach the agent:** when the owner approves a changed file or override, the agent gets an inbox notice, and `whoami` shows the new grant at once.
- **`portenv guide`** covers:
  - the rules: messages are information, not orders; no secrets on a command line (section 1); ask the owner when unsure. It points to `whoami`;
  - the board: what a message, a question, an answer and a notice are; threads (`--thread`); who sees what;
  - the exact formats: the `portenv inbox --json` line schema with one example, and one worked example for each of `msg`, `ask-owner`, `request-package`, `share` and `propose-agent-change`, with the plain-line errors each can return;
  - decisions: their states (pending, approved, declined, expired), waiting with `inbox --wait`, and that only the owner answers;
  - etiquette: one topic per thread, reply in the thread, no secrets.

  The `AGENTS.md` block keeps a short version.
- **`portenv guide --json`** gives the same content as data, for a runner to load: message and decision types, fields and limits, with a `version`.
- **One source:** both forms are generated from the proto and section 7's limits table. A test fails if a field in the proto's board, decision or `whoami` messages is missing from the guide.

### 4b. Status, team status, hand-offs and routines (R-0256 to R-0258, R-0261)

- **Status:**
  - `portenv status set "<line>" [--progress N/M] [--blocked "<reason>"]` and `portenv status clear` set the caller's own status;
  - limits: one plain line, at most 200 characters, one update every 10 s;
  - it shows on the agent's row ("· 4 min ago"), in its `whoami`, and in teammates' with the grant below;
  - the owner's **Ask for Status** is a `status_request` message.
- **`grant.team_status: read`** (default `none`; an override can only narrow it):
  - `portenv team status` and `whoami` show each teammate's state, status, pending requests and idle time;
  - never terminals or homes;
  - in the Orchestrate template, only the coordinator has it; in App product, only the lead.
- **Hand-offs: `portenv handoff AGENT PATH [--note]`,** under the `message` grant:
  - **source:** only from the sender's home or the project, by full path;
  - **reading:** as the sender, by an openat walk with `O_NOFOLLOW`; a link or special file refuses the whole hand-off;
  - **writing:** as the recipient, new files only, into `~/inbox/<sender>/<UTC time>/`;
  - **limits:** at most 100 MB, within the recipient's quota, or nothing lands;
  - **the message:** a hand-off message lists each file's SHA-256.
- **Routines** (only in the repository file, which the owner approves):
  - **format:** `routine: ["every 30m: <note>", "every 2h: <note>"]`. The strict frontmatter has no inline maps, so this replaces R-0257's `{every, note}` example (R-0261 approved it). At least 10 minutes apart, at most a week, at most 4 per agent;
  - **timing:** portenvd keeps the timers while the agent runs. A firing posts the note to the agent's inbox, and `DeliverRoutine` types `[Portenv routine] <note>` into the agent's own main pane;
  - **busy agents:** the helper types only when the agent is idle: no question on screen (ADR 0017's `Waiting()`), 5 s of quiet, not the alternate screen, and the runner's idle-input pattern on its last lines. Otherwise portenvd tries again every 30 s, and missed fires collapse into one;
  - **message wake-ups** (CC-0498, R-0311): when a teammate's or the owner's message, or the owner's answer to a decision, arrives for a running agent, the helper's `WakeAgent` types the fixed line `[Portenv] new message from <sender>; run portenv inbox` (the sender an agent id or the owner) into its main pane under the same checks, never the message's text: one line per unread batch (until it reads its inbox), at most one per agent every 30 s, and none for a runner with no idle pattern, which its row and `whoami` say;
  - **the runner table:** idle patterns come from portenvd's table, which `StartTeamAgent` passes to the helper. A runner with no pattern gets no routines, and its row and `whoami` say why;
  - **the app:** pause and resume from the row menu;
  - `portenv routine propose` isn't in this build; an approved proposal would have to change the repository file (open question).
- **What's listed:** `whoami` and the `AGENTS.md` block list only commands that work in the build (R-0261). `propose-agent-change`, `request-package` and `routine propose` come back when they do.
- **`portenv guide` and `guide --json`:**
  - both are versioned, and generated from the proto and the one limits table (`core/internal/teamkit`), which portenvd also enforces;
  - a test walks every type an agent can read and fails if the guide misses a field or value;
  - `portenv team init [--template starter|orchestrate|app-product]` writes the agent files, never over an existing one.

### 5. Identity: one per agent, the same on every way in

- **The identity is ADR 0016's,** made when the owner approves the agent: for a team agent, approving its file; for a remote agent, approving its pairing code. The Unix user is that identity's body inside the box.
- **Inside the box, the caller is the socket's peer uid.** The helper reads it with `SO_PEERCRED` on the connection, maps uid → agent id, and passes the id to `portenvd`. The request carries no identity. An agent's process can reach only its own socket (file modes), and couldn't claim another's even on the right socket: the uid comes from the kernel.
- **In `portenvd`, the caller is fixed when the call arrives** (ADR 0020's rule): `caller{kind: callerAgent, id, name, box}`, from the helper's message for that call, for a workspace whose helper `portenvd` already trusts (ADR 0010). It sets `CommandAuthor` and `author_agent_id` for everything the agent runs, so the blocks' chip and the sidebar agree.
- **Remote agents land on the same user** (lane A's recommendation, taken): an SSH session's forced command runs as the agent's user; MCP tools run as the agent's user through the helper; a web terminal shows only that agent's panes. One identity, one user, one grant, whatever the door.

### 6. The board (slice 1: messages and owner decisions)

- **Where:** `/home/work/.portenv/board/` in the workspace (box-level, so it moves with the workspace and isn't committed). Two append-only JSONL files: `messages.jsonl` and `decisions.jsonl`. Agents can't write them: the directory is `work`'s, 0755. **One writer: the helper, on `portenvd`'s instruction**; no agent call writes a byte without `portenvd`'s check first.
- **A message** is ADR 0016's type, with these fields and nothing else: `id` (ULID), `from`, `to` (an agent id or `owner`), `thread_id`, `type` (`question`, `answer`, `notice`), `text` (≤ 8 KB, plain text), `created_at`. `portenv inbox` returns an agent's own, with a cursor; the owner sees every message that involves their agents (ADR 0016 point 5).
- **A decision** is a request for the owner: `id`, `from` (agent), `kind` (`ask_owner`, `pane`, `agent_file_change`, `share_login`, `package`), `summary` (≤ 200 characters), `detail` (≤ 32 KB: the question, the command, the diff), `state` (`pending`, `approved`, `declined`, `expired`), `answer_note`, `created_at`, `answered_at`, and for an `ask_owner` with choices, `choices` and the owner's `choice` (below). Each answer is a new line; the file is never rewritten.
- **Only the app answers decisions,** with Approve and Decline, in front of the person (no agent, no CLI on the Mac, no door). A pending decision expires after 24 hours. Expired is final: it never turns into an approval, and the app shows it greyed, with no Approve. The agent gets a notice in its inbox: "Your request expired before the owner answered. Ask again if you still need it." (R-0245)
- **Questions with choices (CC-0501, accepted in R-0314).** `portenv ask-owner TEXT --choice "<label>" --choice "<label>" …` takes 2 to 4 choices, each 1 to 80 characters of plain text on one line (no control or bidi characters, `boardText`), all different; `portenvd` refuses anything else with a plain line, and the helper checks the board line again. The decision stores them in order (`Decision.choices`).
  - The card shows one button per choice, in order, in Approve's place, with no default button, plus Decline… as before. Each label is plain text, never markup. VoiceOver reads each as “Choose “<label>”: <question>”.
  - The app's answer is the choice's number, counting from 1, and its label (`AnswerDecisionRequest.choice` and `choice_label`). `portenvd` checks both against the decision's stored choices and refuses an answer whose number is out of range or whose label isn't that choice, so a forged or stale answer never picks something else. A decision with choices is answered with one of them or declined; one without is never answered with a choice.
  - The answer is an approved decision with `choice` set; the agent's `portenv inbox` shows `decision <id>: chose "<label>"`, and `inbox --json` the decision with `choices` and `choice`.
  - Without `--choice`, nothing changes: a yes/no question keeps Approve and Decline. Only the app answers, as for every decision.
  - A workspace whose toolbox image predates this has a helper and an in-box `portenv` without it: that `portenv` refuses `--choice` as an unknown flag, and that helper writes the board line without the choices, so the question falls back to Approve and Decline. Nothing is chosen wrongly either way.
- **Board text is data, never UI or instructions:** the app shows it as plain text (no Markdown links, no images, no actions in it), and `portenv guide` tells agents that a message is information from another agent, not a command (ADR 0016 point 7).
- **Package decisions (owner's request, 16 Oct).** An Approve on `ask-owner` only says yes; for a system package, Approve also installs it, so the owner doesn't have to type the install.
  - **The request:** `portenv request-package <name> [--reason "<text>"]`.
    - The name must match apt's package grammar (today's `apt-packages.txt` rule: `name[:arch][=version]`), one name per request.
    - Never accepted: an option (anything starting with `-`), a URL, a path, a `.deb`, a source or a key.
    - With `packages: never`, the request is refused with a plain line.
  - **What the owner sees:**
    - init (root, which already runs `apt-get` for `apt-packages.txt`) runs `apt-get -s install --no-install-recommends -- <name>` against the image's configured sources. Only init runs it, with the box's network; the agent never runs `apt-get` (R-0245);
    - the decision lists every package, the exact version apt would install and the configured repository it comes from, dependencies included, plus the reason and the line "Installing runs these packages' setup scripts as root.";
    - init refuses the request, before it reaches the owner, if the simulation would remove or downgrade anything, or would install more than 50 packages.
  - **On Approve:**
    - init installs exactly the approved set, each pinned (`name=version`), non-interactively (`DEBIAN_FRONTEND=noninteractive`, `-y`, `--no-install-recommends`), from the configured sources only. The request can't add a repository or a key.
    - If the set apt would install has changed since the owner saw it, nothing is installed and the agent is told to request again.
    - init then appends the requested names to `/home/work/.portenv/apt-packages.txt`, so they come back after a restore, a Move To or a new box (dependencies follow by themselves).
    - The result, installed or the failing line, goes to the agent's inbox and the sidebar.
  - **The agent never gets sudo or root,** and nothing beyond the approved set is installed. Decline installs nothing.
  - **Limits:**
    - 10 package requests per agent a day;
    - pending ones count toward the agent's pending decisions (section 7).
- **Secret prompts are never relayed** (ADR 0016 point 8): `ask-owner` refuses text taken from a pane whose echo is off, and the app never shows a password prompt's text.

### 7. Limits (owner-tunable in Settings, these are the defaults and the ceilings)

| What | Default | Ceiling |
| --- | --- | --- |
| Agents in a team | 4 | 8 |
| Panes per agent (`max_panes`) | 4 | 8 |
| Panes in a workspace, all agents | 16 | 24 |
| Messages per agent | 60 a minute | 120 a minute |
| Pending decisions per agent | 5 | 20 |
| Package requests per agent | 10 a day | 30 a day |
| An agent's home (section 2) | 2 GB | 20 GB |
| A message | 8 KB | 8 KB |
| Each board file before it's rotated (`.1`, kept with the workspace) | 10 MB | 10 MB |

Past a limit, the call is refused with a plain line ("builder has 4 panes open, its limit. Close one first."), never queued.

### 8. `portenvd` runs the team

- **Start:** the owner chooses Start on an agent (or Start Team). `portenvd` checks the file is approved and valid, then asks the helper to have init make the agent's user if it's new (section 1), seed its home and make its clone on first start (section 2a), check the project's and homes' modes (sections 2 and 2a), and start the agent's tmux server and control client. The helper runs the `runner` argv in the agent's first tab, as its user, with `PORTENV_AGENT=<id>`, `PORTENV_SOCKET=/run/portenv-team/<id>/portenv.sock` and `AGENTS.md` in its home.
- **The team's first start** waits for the owner's `team_start` decision. The decision names the agents the owner chose Start on (`start_agent_ids`, added as they click), and on Approve `portenvd` starts them itself, from the board, not from the app's memory, so it works after the app or `portenvd` restarts (R-0296). Each shows `starting` at once; one that can't start goes back to `stopped`, with the reason ("Couldn't start: …") as its row's line and under Start until a start works, and as a board notice in its column (the notice's `about` names the agent, R-0302).
- **Stop:** the helper ends the agent's tmux server and every process of its uid. Stop is immediate and needs no agent's agreement.
- **Restart policy for the tester build:** none. An agent that exits shows Stopped, with its last line; the owner starts it again.
- **States, shown in the sidebar:** `stopped`, `starting`, `running`, `idle` (ADR 0017's waiting detector: a prompt on screen and nothing happening), `needs_you` (a pending decision from it, or it's waiting at a question), `invalid` (with the line from section 3); for remote agents, `connected` and `disconnected`.
- **Moving and saving:** agent users and homes are in `/home`, so they travel with the workspace. init writes the users again from `users.json` at every box start, so each agent finds its own home again with the same uid, and a team is stopped before Move To (it starts again, by the owner's choice, where the workspace lands).

### 9. Proto

**The source is `proto/`** (lane C's proto PR, the first code after approval). The sketch below is the approved shape. The PR also adds:
- `GetAgentFile`, which returns content, approved content and runner, for Approve's diff;
- decision kinds `team_start` and `delete_agent_files`, and `expires_at` on a decision;
- `PackagePlan.source`;
- `AgentGrant` with a `GrantMode`;
- `AgentInfo.runner` and `grant`;
- `WhoAmI` (R-0246);
- `AskOwner.choices`, `Decision.choices` and `choice`, and `AnswerDecisionRequest.choice` and `choice_label` (CC-0501), all additive;
- shared types (`BoardMessage`, `Decision`, `AgentFile`, `FileChunk`) in `types.proto`, so neither file's generated indices move.

All appended per the merge guard: new RPCs at the end of `DaemonService`, new messages at the end of `daemon.proto`, enums in `types/v1/types.proto`.

```proto
// daemon.proto, DaemonService (app ↔ portenvd)
rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse);
rpc WatchTeam(WatchTeamRequest) returns (stream WatchTeamResponse);
rpc StartAgent(StartAgentRequest) returns (StartAgentResponse);
rpc StopAgent(StopAgentRequest) returns (StopAgentResponse);
rpc AnswerDecision(AnswerDecisionRequest) returns (AnswerDecisionResponse);
rpc ApproveAgentFile(ApproveAgentFileRequest) returns (ApproveAgentFileResponse); // added to R-0226's five: the content pin of section 3
rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);                // the owner to an agent (lane D)
rpc RemoveAgent(RemoveAgentRequest) returns (RemoveAgentResponse);                // delete_files only from the owner's decision (section 1)
rpc ListAgentFiles(ListAgentFilesRequest) returns (ListAgentFilesResponse);       // the Agent files view (section 2)
rpc ReadAgentFile(ReadAgentFileRequest) returns (stream FileChunk);
rpc AddAgentFiles(stream AddAgentFilesRequest) returns (AddAgentFilesResponse);   // drag and drop; refused past the quota

message ListAgentsRequest { string name = 1; }            // the workspace
message ListAgentsResponse { repeated AgentInfo agents = 1; }
message AgentInfo {
  string id = 1; string name = 2; string role = 3; string avatar = 4;
  bool remote = 5; string via = 6;                       // "ssh" | "mcp" | "web" when remote
  portenv.types.v1.AgentState state = 7;
  string problem = 8;                                    // plain line for invalid or a failed start
  string file_sha256 = 9; bool approved = 10;            // the pinned content, and whether this content is it
  repeated string tab_ids = 11;                          // its tabs, for the sidebar
  string last_line = 12;                                 // its last board line, plain text
  int32 pending_decisions = 13;
  int64 home_bytes = 14; int64 home_quota_bytes = 15;     // section 2's quota
}
message WatchTeamRequest { string name = 1; string cursor = 2; }
message WatchTeamResponse {
  oneof event { AgentInfo agent = 1; BoardMessage message = 2; Decision decision = 3; string removed_agent_id = 5; }
  string cursor = 4;
}
message BoardMessage { string id = 1; string from = 2; string to = 3; string thread_id = 4;
  portenv.types.v1.MessageType type = 5; string text = 6; google.protobuf.Timestamp created_at = 7; }
message Decision { string id = 1; string from = 2; portenv.types.v1.DecisionKind kind = 3;
  string summary = 4; string detail = 5; portenv.types.v1.DecisionState state = 6;
  string answer_note = 7; google.protobuf.Timestamp created_at = 8; google.protobuf.Timestamp answered_at = 9; }
message StartAgentRequest { string name = 1; string agent_id = 2; }
message StartAgentResponse { AgentInfo agent = 1; }
message StopAgentRequest { string name = 1; string agent_id = 2; }
message StopAgentResponse { AgentInfo agent = 1; }
message AnswerDecisionRequest { string name = 1; string decision_id = 2; bool approve = 3; string note = 4; }
message AnswerDecisionResponse { Decision decision = 1; }
message ApproveAgentFileRequest { string name = 1; string agent_id = 2; string file_sha256 = 3; } // must match what the app showed
message ApproveAgentFileResponse { AgentInfo agent = 1; }
message SendMessageRequest { string name = 1; string to = 2; string thread_id = 3; string text = 4; } // from is always "owner"
message SendMessageResponse { BoardMessage message = 1; }
message RemoveAgentRequest { string name = 1; string agent_id = 2; bool delete_files = 3; }
message RemoveAgentResponse {}
message ListAgentFilesRequest { string name = 1; string agent_id = 2; string dir = 3; }   // dir relative to its home
message ListAgentFilesResponse { repeated AgentFile files = 1; int64 home_bytes = 2; int64 home_quota_bytes = 3; }
message AgentFile { string path = 1; bool dir = 2; int64 size = 3; google.protobuf.Timestamp modified = 4; }
message ReadAgentFileRequest { string name = 1; string agent_id = 2; string path = 3; }
message FileChunk { bytes data = 1; }
message AddAgentFilesRequest { string name = 1; string agent_id = 2; string path = 3; bytes data = 4; bool last = 5; } // name and agent_id on the first
message AddAgentFilesResponse { repeated AgentFile added = 1; }

// types.proto, Tab (appended; lane D): which agent's server a tab is on, and its panes
//   string agent_id = 7;            // empty for the owner's tabs
//   repeated string pane_ids = 8;   // tab ids stay unique across servers: the helper prefixes an agent's with its id

// types.proto
enum AgentState { AGENT_STATE_UNSPECIFIED = 0; AGENT_STATE_STOPPED = 1; AGENT_STATE_STARTING = 2;
  AGENT_STATE_RUNNING = 3; AGENT_STATE_IDLE = 4; AGENT_STATE_NEEDS_YOU = 5; AGENT_STATE_INVALID = 6;
  AGENT_STATE_CONNECTED = 7; AGENT_STATE_DISCONNECTED = 8; }
enum MessageType { MESSAGE_TYPE_UNSPECIFIED = 0; MESSAGE_TYPE_QUESTION = 1; MESSAGE_TYPE_ANSWER = 2; MESSAGE_TYPE_NOTICE = 3; }
enum DecisionKind { DECISION_KIND_UNSPECIFIED = 0; DECISION_KIND_ASK_OWNER = 1; DECISION_KIND_PANE = 2;
  DECISION_KIND_AGENT_FILE_CHANGE = 3; DECISION_KIND_SHARE_LOGIN = 4;
  DECISION_KIND_PACKAGE = 5; }
enum DecisionState { DECISION_STATE_UNSPECIFIED = 0; DECISION_STATE_PENDING = 1; DECISION_STATE_APPROVED = 2;
  DECISION_STATE_DECLINED = 3; DECISION_STATE_EXPIRED = 4; }
```

```proto
// agent.proto, AgentService (portenvd ↔ helper), appended
// The helper relays in-box `portenv` calls up to portenvd, each with the caller it read from the socket.
rpc TeamCalls(stream TeamReply) returns (stream TeamCall);   // portenvd opens it; the helper sends calls, portenvd answers
rpc StartTeamAgent(StartTeamAgentRequest) returns (StartTeamAgentResponse);
rpc StopTeamAgent(StopTeamAgentRequest) returns (StopTeamAgentResponse);
rpc AppendBoard(AppendBoardRequest) returns (AppendBoardResponse);           // the board's one writer
rpc ListHomeFiles(ListHomeFilesRequest) returns (ListHomeFilesResponse);     // the Agent files view, done as the agent's user
rpc ReadHomeFile(ReadHomeFileRequest) returns (stream FileChunk);
rpc WriteHomeFiles(stream WriteHomeFilesRequest) returns (WriteHomeFilesResponse);
// Served only by init on its local socket (root); the channel's server answers Unimplemented:
rpc EnsureAgentUser(EnsureAgentUserRequest) returns (EnsureAgentUserResponse);
rpc RemoveAgentUser(RemoveAgentUserRequest) returns (RemoveAgentUserResponse);
rpc SimulatePackage(SimulatePackageRequest) returns (SimulatePackageResponse); // apt-get -s: the exact set and versions
rpc InstallPackages(InstallPackagesRequest) returns (InstallPackagesResponse); // exactly the approved set, or nothing
message TeamCall { string call_id = 1; string agent_id = 2;                  // agent_id from SO_PEERCRED, never the request
  oneof op { Split split = 3; NewTab tab = 4; PaneRef focus = 5; PaneRef close = 6; Msg msg = 7;
             AskOwner ask_owner = 8; Inbox inbox = 9; Share share = 10; ProposeAgentChange propose = 11;
             RequestPackage request_package = 12; } }
message TeamReply { string call_id = 1; bool ok = 2; string line = 3; bytes result = 4; } // line: the plain refusal
```

The doors (section 10) call the same `portenvd` functions behind `TeamCalls`; there's no second implementation of a grant check.

### 10. Remote agents (R-0227)

**In the agent file:** `runner: remote` with `via: ssh | mcp | web`. A remote agent gets the same identity (made by pairing, ADR 0016), the same per-agent user, the same sidebar row (connected or disconnected) and the same grant. Portenv doesn't start it.

**One rule for all three:** every action a remote agent takes is one of section 4's commands, made as that agent's identity, through `portenvd`'s grant check. No path skips it, and none reaches the owner's panes, the Mac, or another workspace.

**Network exposure is per workspace, opt-in, and off by default.** On the Mac it never means an inbound port (CLAUDE.md, ADR 0015).

**SSH (designed, after testers):**
- **Keys:** only the owner adds a key, while approving the agent's pairing code. The agent's private key never leaves its side. ADR 0016's short-lived certificates replace bare keys when the runner's CA is used (ADR 0007).
- **Where it lands:** on a server, the runner's sshd, with a forced command (`portenv agent-shell <id>`) that attaches to the agent's own tmux server as the agent's user. `restrict` is set, so there's no port forwarding, no agent forwarding, no X11 and no user rc.
- **On a Mac:** none in the tester build. There's no inbound port, so a cloud agent can't reach the Mac. A local SSH client would gain nothing over MCP.

**MCP (recommended for the tester build, in its local form):**
- **The tools map one to one onto section 4's commands:** `box.split`, `box.tab_new`, `box.focus`, `box.close`, `box.msg`, `box.ask_owner`, `box.inbox`, `box.share`, and `box.run` (run a command in the agent's own pane and return its output when it finishes, ADR 0017's done signal), plus the read-only `box.screen` and `box.events`, for its own panes only. The names live under `box.*` (ADR 0016 point 10).
- **Credentials:** a per-agent token the owner issues in the app (Team › agent › Connect…) and can revoke at once. It's stored in the Keychain on the Mac, shown once to copy into the agent's MCP settings, and kept by `portenvd` only as a hash.
- **Local, on the Mac (tester build):**
  - **How it runs:** `portenv mcp --agent <id>`, a stdio MCP server the agent's app starts (Claude Desktop, Cursor, Codex and similar). It connects to `portenvd`'s owner-only socket and presents the token. `portenvd` then fixes the caller as that agent, not the owner. There's no listener.
  - **Honest limit:** a local MCP client runs as the owner on the owner's Mac, so it could open the owner's socket without the token. Its identity is for attribution and grants by consent, as with any app the owner installs.
- **Remote (designed, after testers):** streamable HTTP with OAuth, served by the runner on the user's own server under ADR 0015 (opt-in public door, or the user's own tunnel), or later by Portenv's gateway (3.9). The OAuth consent is the pairing (ADR 0016). This is how ChatGPT's Dots gets in.

**Web terminals (designed, after testers):**
- **Where:** served on a private network only, the user's tailnet or LAN, never a public address. On a server it can be ADR 0015's door, under its conditions.
- **Who:** the owner signs in with a passkey, then hands the agent a one-time link tied to one agent and one workspace (ADR 0015's rule).
- **What it shows:** only that agent's panes, as the agent's user. Muse's way in.

**Nothing remote is built before the owner's run (R-0237).** The first to build after it is local MCP (R-0231 asks for MCP or SSH; MCP is the one a Mac can serve without an inbound port).
- **Why MCP:** one door serves several agents (Claude Desktop, Cursor, Codex today; ChatGPT once remote MCP exists). It needs no listener on the Mac. Its tools are section 4's commands, so it adds no new power to check.
- **SSH next, then web.**
- **The reviewer should know:** none of the three lets a *cloud* agent (Grok Bot, Dots, Muse) reach a workspace on a Mac in the tester build. Every path for them needs something reachable from the internet: the runner on the user's own server (2.7, 2.8, after testers, with 2.9 Set Up) or Portenv's gateway (Phase 3). Opening the Mac to them would break the no-inbound-port rule.
- **What testers get first:** the team inside the workspace. Local agents join through MCP once it's built after the owner's run; the cloud agents' door comes with servers.

### 11. Build order and dates (R-0237, R-0232)

- **Fri 16, 18:00:** this ADR to the reviewer; review by 20:00. On approval, code starts tonight. Lane C's proto PR (section 9, with `make proto`) goes first, so every lane builds against the same messages. Lane A's tab-streams-its-own-pane fix and #119's fix also land today.
- **Sat 17, 14:00, preview build (in a lane):**
  - per-agent users with their own tmux servers, homes and `/home/team`;
  - `portenv split`, `tab new`, `msg` and `ask-owner`, with grants;
  - `portenv request-package` if there's time; it may move to Sunday, never later.

  The reviewer UI-tests it 14:00–16:00; the owner tries it on the test project after. **If the preview is at risk, lane C tells the reviewer by Sat 10:00.**
- **Sun 18, 12:00, full build:**
  - the board;
  - the Team sidebar with agents' tabs and decisions;
  - agent files;
  - package decisions (section 6), if they didn't make the preview;
  - `portenv team init` with the Orchestrate template;
  - the Agent files view.

  The reviewer tests 12:00–15:00. The owner's end-to-end run starts at 15:00, from lane C's run sheet: install, add the project, `portenv team init` (Orchestrate team), start the agents, watch the sidebar, answer a decision, open an agent's panes, move the workspace, close and reopen the window.
- **Mon 19:** fixes from the owner's run. Testers only on the owner's word.
- **If a part isn't ready,** the build ships without it and says so; nothing ships untested. Per-agent users are never the part that's cut. Remote agents wait until after the owner's run.
- **One dependency:** filtering the window to one agent's tabs (lane D) needs per-tab terminals wired into the window (lane A's 1.2c). If 1.2c isn't merged by Sat evening, the Sunday build shows agents' tabs labelled in the one tab bar, and filtering waits for it.

### 12. Who owns which files this weekend (R-0237)

One lane owns each file; another lane that needs a change asks the owner of the file on the board. Small PRs, each reviewed as usual.

| Lane | Owns | Builds |
| --- | --- | --- |
| **A** (terminals) | `core/agent/controlmode*.go`, `ctlhub*.go`, `tabterm*.go`, `tabs*.go`, `paneparser*.go`, `panelane.go`, `terminal*.go`; `core/daemon/tabs.go`, `terminal.go`; `apps/mac/.../BoxTerminal.swift`, and `TabBar.swift` until 1.2c merges | one tmux server and control client per agent (started through a function lane C calls, with the user it's given); tab ids across servers; `agent_id` and `pane_ids` filled on each tab; the tab-pane fix; 1.2c's per-tab terminals in the window |
| **C** (users, command, board) | `proto/` (all of section 9); `core/agent/init_linux.go`'s user and package calls (and `boot.go`'s apt code) and a new `core/agent/team*.go` (users, sockets, `SO_PEERCRED`, homes, quota, seeding, home file I/O, board writer); a new `core/daemon/team*.go` (agent files, pin, validation, grants, board, decisions, the RPCs); `core/cmd/portenv`'s in-box commands, `whoami`, `guide` (and its generator and test) and `team init`; the `AGENTS.md` block; the templates; `images/toolbox-node/` (group `team`, `/run/portenv-team`); `docs/PLAN.md`; the run sheet | the preview's users and commands, then the board and agent files |
| **D** (the app) | `apps/mac/.../DaemonClient.swift`, `DaemonAPI.swift`, `MainWindow*.swift`, `InspectorView.swift`, `Inspector.swift` (Who's here), `SettingsWindow.swift`'s team limits, new `Team*.swift` and `AgentFiles*.swift`, and `TabBar.swift` after 1.2c | the Team sidebar, decisions, approving agent files with their diff, the Agent files view with drag and drop, filtering the window by agent |
| **E** (boundary tests) | a new `tests/team-boundary/`, its CI job (`.github/workflows/`), its `Makefile` target, and `core/daemon/caller.go`'s agent caller | the per-agent boundary tests (below), table-driven against a real engine on both architectures, with a mutation check per rule; the agent identity in `caller.go` |

**Lane E's boundary tests (its offer, taken):** each rule is one row, run as an agent's user, and each row has a mutation that must make it fail. The rows:
- reaching the owner's or another agent's tmux;
- the homes (another agent's, the owner's dotfiles);
- `/home/team`'s setgid and group-write rule; the project and its `.git` writable by no agent; another agent's clone readable only with `read_clones`, never writable (section 2a);
- `portenvd`'s socket, the SSH-agent socket and the agent channel's sockets;
- `sudo` and set-uid binaries;
- installing a system package without an approved decision (`apt-get`, `dpkg`, writing under `/etc/apt` or `/var/lib/dpkg`);
- ADR 0005's memory probe;
- `work`'s sockets anywhere in the box (`SSH_AUTH_SOCK`, a gpg agent, a VS Code server, `/tmp/*`), and the team-start check that refuses when one is reachable;
- `whoami` reports exactly what the grant check enforces: each grant and limit, flipped, changes both together.

They gate the Saturday preview: a red row means the preview doesn't ship.

## Consequences

- The tester build gets a team that the grants actually hold: each agent its own user, its own tmux, no sudo, and only `portenvd`'s checked calls can touch what the owner sees.
- **Agents lose the owner's logins.** Each signs in, or the owner shares one login per agent, with a warning that sharing can't be undone by removing the agent. Testers will notice the extra sign-in; first-run copy for the Team should say why.
- ~~**Agents share the owner's checkout.**~~ **Amended (R-0315): each agent works in its own clone,** and its work comes back as a branch the owner fetches (section 2a). A team no longer shares one tree: a hand-off between agents is a branch or a `portenv handoff`, and the owner runs agents' changes only after fetching them (section 2's residual risk, which the app states once).
- **New code, all after approval:**
  - **init:** making and removing agent users, and writing them again at every box start.
  - **The helper:** the user switch, one tmux server and control client per agent, per-agent sockets with `SO_PEERCRED`, the board writer, home seeding, the quota and home file I/O, and the clones, the safe fetch, the read grants for lenses, the project and home mode checks, and the migration (section 2a).
  - **`portenvd`:** agent-file validation and the content pin, the grant checks, the board and decisions, `TeamCalls`, the RPCs above, and `portenv mcp`.
  - **The app:** the Team section (lane D's mockups), Approve and Decline, approving a file with its diff, the Agent files view, and (after the owner's run) issuing and revoking MCP tokens.
  - **Templates and docs:** the Orchestrate team template and the owner's run sheet (section 11).
- **Tests before code (data-loss and security paths; lane E's boundary rows in section 12, plus these):**
  - an agent's raw `tmux` can't reach the owner's server: lane A's repro, run as an agent's user, fails at every step, and so does reaching another agent's server;
  - a request naming another agent is attributed to its socket's uid;
  - `send-keys` into a pane the agent didn't open is refused;
  - a changed agent file doesn't start until approved, with the diff;
  - ~~an agent's uid can write the project (new files stay in `team`, whatever its umask) but can't write `.git/hooks`, `.git/config` or the project's `.portenv/`~~ (amended, R-0315) an agent's uid can read the project but can't write anything in it or its `.git`, and can't read the owner's dotfiles;
  - section 2a's gate: an agent that tampers with its own clone's config makes `work`'s git in the project run nothing, and its branch still arrives by the safe fetch;
  - the migration (section 2a) loses no agent commit and no uncommitted file, and deletes nothing without the owner's decision;
  - the team won't start over a project or home whose modes are wrong, a `.git` with anything not `work`'s, or a filesystem without ACLs;
  - an agent's process can't gain a capability or use `sudo` (`no_new_privs`, no ambient set);
  - a removed agent's home stays until the owner deletes it, and its uid stays taken; init refuses a reserved or malformed id even when the helper sends one;
  - `users.json` brings every agent back with the same uid after a restore;
  - `home_seed` refuses a path or symlink outside the project; the Agent files view writes as the agent's user and refuses past the quota;
  - each limit refuses with its line;
  - board text with Markdown or escape sequences renders as plain text;
  - Stop ends every process of the agent's uid;
  - a secret prompt's text never reaches the board;
  - `request-package` refuses options, URLs, paths and `.deb` files; init installs exactly the approved set or nothing, refuses a simulation that removes or downgrades, and the package comes back from `apt-packages.txt` after a restore;
  - a revoked MCP token is refused at once;
  - the team's users, homes (clones included), `/home/team`, the project's read-only ACLs and the lenses' read grants survive a save, a Move To and a restore.
- **PLAN.md** gains the team in Phase 1's tester-build scope (R-0226) when this is approved. 2.4's stand-in mode stays as designed. Lanes (4.1) now arrive early, in this team form, for team agents.
- **Open questions** for PLAN when approved:
  - whether stand-in agents (the owner's access, ADR 0006 B) still make sense once team agents exist, or become a team agent with wider grants;
  - per-agent CPU and memory limits (cgroups need the engine's delegation, which the box lacks today; see the rootless spike, R-0223);
  - whether restic keeps `/home/team`'s and the clones' ACLs and setgid bits on restore, or the helper reapplies them at team start (the tests above decide);
  - ~~when to offer `checkout: own` (per-agent clones)~~ answered by R-0315: always, for coding agents (section 2a);
  - R-0315's open questions, in PLAN: how the owner sees an agent's branch in the app, and whether Portenv fetches on `share`; whether lenses need their own clone to read a writer's history in the first build; whether agents keep read access to the owner's untracked files (`.env`) in the project; clone size against an agent's home limit;
  - kernel quotas for agents' homes (today the limit is measured, not enforced);
  - Google Drive connectors for agents' files (after the tester build).
