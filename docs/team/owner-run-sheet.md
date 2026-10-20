<!-- SPDX-License-Identifier: Apache-2.0 -->

# The agent team: the owner's run sheet

The end-to-end run before testers (R-0232), on a project of your own. Each
step says what you should see. If you don't, stop there and say which step.

1. **Install.** Open the release-candidate build of Portenv. Welcome shows
   Portenv's icon; the state line says it's ready.
2. **Add the project.** Make a workspace for it, and put the project in
   `/home/work/<workspace name>` (clone it there in the workspace's
   terminal). The Team section says the project has no team yet.
3. **Make the team.** In the workspace's terminal, in the project folder:
   `portenv team init --template orchestrate`. It writes six agent files
   (coordinator, reviewer, builder-a, builder-b, builder-c, devops) to
   `.portenv/agents/`, and the design keeper to `.portenv/agents/off/`,
   which Portenv doesn't load; it says nothing runs before you approve
   each. Edit them if you like: the instructions are generic on purpose.
   The coordinator has a routine (`every 30m: …`) and
   `team_status: read`.
4. **Approve the agent files.** In the Team section, open each agent. Its
   `runner` line is shown first, then the file; choose Approve. A file you
   change later needs approving again.
5. **Start the agents.** Choose Start on the coordinator. The first start asks you
   once to approve starting the team: it says agents can change the
   project's files, that they pass through your home folder (0711), and
   that Stop Team doesn't change that back. Approve, then start the others.
   If Portenv refuses because a private folder (like `~/.ssh`) is readable
   by others, fix that and start again.
6. **Watch the sidebar.** Each agent has a row with its state and its
   status line ("Running the tests · 2 min ago"); its own tabs open under
   it. Ask an agent for its status from its row menu.
7. **Answer a decision.** Give the coordinator a task that needs a new
   pane (for example, running the tests). With `panes: ask`, a decision
   appears; Approve it, and the pane opens in your window, labelled with
   the agent. Decline another one, and nothing opens.
8. **Open an agent's panes.** Click an agent's tab: you see its terminal.
   You can read and type in it; agents can't read or type in yours or each
   other's.
9. **Hand files over.** Ask builder-a to hand a file to the coordinator
   (`portenv handoff coordinator <path>`). It lands in the coordinator's
   `~/inbox/builder-a/<time>/`, and the message lists each file's SHA-256.
   Drag a file onto an agent in Agent files: it lands in that agent's home.
10. **Move the workspace.** Use Move To, as you would without a team: the
    team stops first. Where it lands, start the agents again; their homes,
    files and the board are all there.
11. **Close and reopen the window.** The workspace and the agents keep
    running; reopening shows the same rows, statuses and the board.

When you've done all eleven, say so in chat: testers get the build only on
your word.
