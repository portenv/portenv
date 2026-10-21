# 0028. Workspace tabs: each workspace a macOS window tab

Date: 2026-10-19 · Status: Accepted (the owner's request, R-0344; the reviewer's decision, R-0345). For workspace windows only, it supersedes R-0081's "macOS's own window tabs are off". Terminal tabs stay Portenv's own tab bar.

## Context

Until now the app showed one workspace at a time. A single `AppModel` held the open workspace's controller, tabs, team and panes, and opening another workspace replaced them. R-0081 turned macOS's window tabs off because Portenv's tab bar was the only tabs, and a native tab's close went around the close hook. Since #156 (ADR 0023), closing a window doesn't stop its workspace. The owner wants several workspaces open side by side: "another tab beside it, nothing else changes" (R-0344).

## Decision

1. **Native window tabs for workspaces.** Each open workspace has a window of its own, and workspace windows group as macOS window tabs in one tabbing group (`workspace`, `tabbingMode = .preferred`).
   - **Why native:** macOS gives drag-out to a window, dragging back in, Merge All Windows, Move Tab to New Window, Show Next/Previous Tab, the ＋ button, Mission Control and Full Keyboard Access for free.
   - **Why it fits the owner's words:** a workspace in a tab looks and works exactly as a workspace in a window did, with another tab beside it. Nothing else in the window changes.
   - **Which windows:** Settings, Acknowledgements and the window showing first run stay `.disallowed`, so they never become tabs.
   - **One frame:** a window joining the group takes the group's frame; no default, ideal or minimum size of its content resizes it, and New Workspace… in a tab never places its window as first run does (the owner's dev-build test, 2026-10-21).
   - **The close hook:** each window's close button still goes through `WindowCloseHook`, now per window.
2. **One session per window.** Each window owns a `WorkspaceSession`: its `BoxController`, `TabsController`, `TeamController` and `SplitController`, so the selected agent, the current terminal tab and the split layout are that window's own. `WorkspaceSessions` lists them. The first window shows the launch (the wait, first run, the list with its notice). A new tab (＋, File › Open Workspace… ⌘N) shows "Your workspaces". Opening a workspace that's already open brings its tab forward; it never opens twice, and a new tab that was only picking it goes.
3. **The menu bar acts on the main window's session** (`WorkspaceSessions.current`, set when a workspace window becomes main), AppKit's own menu target, rather than `@FocusedValue`. That covers Workspace, tab, split, inspector and sidebar commands. The menus keep working while Settings is key or Portenv is behind (R-0061), and the target can be unit-tested. The title menu (⌄) stays its own window's workspace's.
4. **Close, quit, wake, relaunch** (#156, ADR 0023):
   - **Close:** closing a tab closes that window only. Its session stops following, and the workspace keeps running in portenvd. Closing the last workspace window still goes back to "Your workspaces". A tab behind another in its group counts as an open window.
   - **Quit:** unchanged. One confirmation names every running workspace (QuitAll), then each is closed; Quit's list of known workspaces includes every session's.
   - **Wake:** checks every session's workspace.
   - **Update relaunch:** leaves every one running and tells portenvd once.
   - **Keychain:** a Keychain prompt brings forward the tab whose workspace asked (R-0214).
   - **Notifications:** a workspace's window counts as in front only when its own tab is.
5. **The tab's title** is the workspace's name, where it runs and the decisions waiting for you (`acme-api · This Mac · 2`), with the count named in the tooltip. A tab with no workspace yet (the list, New Workspace…) is "New workspace" (the owner's dev-build test, 2026-10-21). The count comes from the team each session already follows (WatchTeam). The session follows its team from open to close, so a background tab stays current. No portenvd or proto change.
6. **Shortcuts:**
   - Terminal tabs keep ⌘1–⌘9 and ⌘⇧[ / ⌘⇧]. In the Window menu they're named Show Previous/Next Terminal Tab and Show Terminal Tab ▸.
   - Workspace tabs use ⌃⌘1–⌃⌘9 (Window › Show Workspace Tab ▸; past the last tab, nothing), and ⌃Tab / ⌃⇧Tab (macOS's Show Next Tab and Show Previous Tab).
   - ⌃Tab used to switch terminal tabs on any keyboard layout. On keyboards without bracket keys (AZERTY), ⌘1–⌘9 and the Window menu still reach every terminal tab.

## Limitations

- **Window restoration:** with macOS's "Close windows when quitting an application" turned off (it's on by default), macOS may restore several windows. The first follows the launch decision; the others show "Your workspaces". Per-window `SceneStorage` reopening can come later.

## Options not taken

- **B. Portenv's own workspace strip** (a second tab bar above the terminal tabs, inside one window). It would allow custom badges and layout, but it means building drag-out, merge and keyboard access ourselves, and two app-drawn tab bars stacked. It's post-tester, and only if the owner asks for it.

## Tests

`WorkspaceTabsTests` covers:
- sessions, and a workspace already open focusing its tab
- view state kept per session
- the menu target
- quit and wake covering every session
- tab titles with and without needs-you, local and server
- the ⌃⌘n mapping

`WindowCloseHookTests` covers only workspace windows being tabs, and a background tab counting as open. UI checks are the reviewer's (PR #227).
