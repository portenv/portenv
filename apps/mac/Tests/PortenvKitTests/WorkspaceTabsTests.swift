// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import Testing

@testable import PortenvKit

/// Workspace tabs (R-0344, R-0345; GUIDELINES §3.3): each workspace in its
/// own window, grouped as macOS window tabs, each window with its own
/// session (controller, tabs, team, panes); the menus act on the session
/// whose window is main; ⌃⌘1–⌃⌘9 pick a tab; the tab's title says where
/// the workspace runs and how many decisions wait for you.
@MainActor
@Suite(.serialized)
struct WorkspaceTabsTests {
    func session(_ daemon: FakeDaemon) -> WorkspaceSession {
        WorkspaceSession(daemon: daemon)
    }

    func window() -> NSWindow {
        _ = NSApplication.shared
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 400, height: 300),
                         styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: true)
        w.isReleasedWhenClosed = false
        return w
    }

    // MARK: Sessions

    /// A window's session starts on the list (or the launch, for the first
    /// window); showing a workspace gives it its own controllers.
    @Test func showingAWorkspaceGivesTheSessionItsOwnControllers() {
        let d = FakeDaemon()
        let s = session(d)
        #expect(s.screen == .list)
        #expect(s.box == nil && s.controller == nil && s.tabs == nil && s.split == nil && s.team == nil)
        #expect(WorkspaceSession(daemon: d, screen: .launch).screen == .launch)

        s.show("acme-api")
        #expect(s.screen == .workspace)
        #expect(s.box == "acme-api")
        #expect(s.controller?.box == "acme-api" && s.tabs?.box == "acme-api" && s.team?.box == "acme-api")
        #expect(s.split != nil)

        // Back to the list: everything the window held goes; the workspace
        // itself keeps running in portenvd (nothing is closed here).
        s.leave()
        #expect(s.screen == .list)
        #expect(s.box == nil && s.controller == nil && s.tabs == nil && s.split == nil && s.team == nil)
        #expect(!d.calls.contains { $0.count > 1 && $0[1] == "close" })
    }

    /// Two windows, two workspaces: switching between them never mixes
    /// their controllers, and opening one that's already open focuses its
    /// tab instead of opening it twice.
    @Test func aWorkspaceAlreadyOpenFocusesItsTab() {
        let d = FakeDaemon()
        let sessions = WorkspaceSessions()
        let a = session(d), b = session(d), picker = session(d)
        sessions.add(a)
        sessions.add(b)
        sessions.add(picker)
        #expect(sessions.choose("acme-api", for: a) == .here)
        a.show("acme-api")
        #expect(sessions.choose("web", for: b) == .here)
        b.show("web")

        #expect(sessions.showing("acme-api") === a)
        #expect(sessions.showing("web") === b)
        #expect(sessions.showing("docs") == nil)
        #expect(a.controller !== b.controller && a.tabs !== b.tabs && a.team !== b.team && a.split !== b.split)

        // From the new tab's list: acme-api is open in a, so a's tab comes
        // forward and nothing opens twice.
        #expect(sessions.choose("acme-api", for: picker) == .focus(a))
        // The window already showing it stays on it.
        #expect(sessions.choose("acme-api", for: a) == .here)
        // Once a goes back to the list, acme-api opens wherever it's asked.
        a.leave()
        #expect(sessions.choose("acme-api", for: picker) == .here)
        #expect(sessions.showing("acme-api") == nil)
    }

    /// Each session keeps its own view state: the selected agent, the tab
    /// and the panes; another window's choices never change them.
    @Test func viewStateIsKeptPerSession() async {
        let d = FakeDaemon()
        let sessions = WorkspaceSessions()
        let a = session(d), b = session(d)
        sessions.add(a)
        sessions.add(b)
        a.show("acme-api")
        b.show("web")
        a.tabs?.receive([TabInfo(id: "1", name: "shell", active: true), TabInfo(id: "2", name: "tests", active: false)])
        b.tabs?.receive([TabInfo(id: "7", name: "server", active: true)])
        a.split?.size = CGSize(width: 1400, height: 900)
        a.split?.tabsChanged()

        await a.split?.split(.right)
        #expect(a.split?.isSplit == true)
        a.team?.selection = .agent("builder")
        _ = a.tabs?.view(.agent("builder"))

        // Switching windows (the menus' target) keeps both as they were.
        let wa = window(), wb = window()
        a.window = wa
        b.window = wb
        sessions.becameMain(wb)
        sessions.becameMain(wa)
        #expect(a.team?.selection == .agent("builder"))
        #expect(a.tabs?.selection == .agent("builder"))
        #expect(a.split?.isSplit == true)
        #expect(b.team?.selection == .allTabs)
        #expect(b.tabs?.selection == .allTabs)
        #expect(b.split?.isSplit == false)
        #expect(b.tabs?.tabs.map(\.id) == ["7"])
        a.leave()
        b.leave()
    }

    /// The menus act on the session whose window is main (the menu bar's
    /// target in AppKit): it follows the windows as they come forward, a
    /// window that isn't a workspace window (Settings) doesn't change it,
    /// and closing the main one moves it to another session.
    @Test func menusTargetTheFocusedSession() {
        let d = FakeDaemon()
        let sessions = WorkspaceSessions()
        #expect(sessions.current == nil)
        let a = session(d), b = session(d)
        let wa = window(), wb = window(), settings = window()
        a.window = wa
        b.window = wb
        sessions.add(a)
        #expect(sessions.current === a)
        sessions.add(b)
        a.show("acme-api")
        b.show("web")

        sessions.becameMain(wb)
        #expect(sessions.current === b)
        #expect(sessions.current?.controller?.box == "web")
        sessions.becameMain(settings)
        #expect(sessions.current === b)
        sessions.becameMain(wa)
        #expect(sessions.current?.controller?.box == "acme-api")

        sessions.remove(a)
        #expect(sessions.current === b)
        #expect(!sessions.all.contains { $0 === a })
        sessions.remove(b)
        #expect(sessions.current == nil)
    }

    /// Quit and Wake cover every open session; Quit names every workspace
    /// open in a window as well as those portenvd last said were running.
    @Test func everySessionCountsForQuitAndWake() {
        let d = FakeDaemon()
        let sessions = WorkspaceSessions()
        let a = session(d), b = session(d), list = session(d)
        [a, b, list].forEach(sessions.add)
        a.show("acme-api")
        b.show("web")
        #expect(sessions.controllers.map(\.box) == ["acme-api", "web"])
        // None is open yet: only what portenvd said.
        #expect(sessions.knownRunning(also: ["docs"]) == ["docs"])
        a.leave()
        b.leave()
    }

    // MARK: Tab title

    /// The tab's title: the workspace, where it runs, and the decisions
    /// waiting for you when there are any (R-0344).
    @Test func theTabTitleSaysWhereAndHowManyNeedYou() {
        #expect(WorkspaceTabTitle.text(box: "acme-api", location: .thisMac, needsYou: 0) == "acme-api \u{00B7} This Mac")
        #expect(WorkspaceTabTitle.text(box: "acme-api", location: .thisMac, needsYou: 2) == "acme-api \u{00B7} This Mac \u{00B7} 2")
        #expect(WorkspaceTabTitle.text(box: "web", location: .server("portenv@test-server"), needsYou: 0) == "web \u{00B7} test-server")
        #expect(WorkspaceTabTitle.text(box: "web", location: .server("portenv@test-server"), needsYou: 1) == "web \u{00B7} test-server \u{00B7} 1")
        // Not open yet (opening, or closed): just its name.
        #expect(WorkspaceTabTitle.text(box: "acme-api", location: .closed, needsYou: 0) == "acme-api")
        #expect(WorkspaceTabTitle.text(box: "acme-api", location: .closed, needsYou: 3) == "acme-api \u{00B7} 3")
        // The tooltip and VoiceOver spell the count out.
        #expect(WorkspaceTabTitle.help(box: "acme-api", location: .thisMac, needsYou: 2) == "acme-api \u{00B7} This Mac \u{00B7} Needs you \u{00B7} 2")
        #expect(WorkspaceTabTitle.help(box: "web", location: .server("portenv@test-server"), needsYou: 0) == "web \u{00B7} test-server")
    }

    /// The session's title follows its own team: a decision waiting in a
    /// workspace whose tab isn't in front still counts, from the team that
    /// session watches.
    @Test func theSessionTitleCountsItsOwnDecisions() {
        let d = FakeDaemon()
        let a = session(d), b = session(d)
        #expect(a.tabTitle == nil)
        a.show("acme-api")
        b.show("web")
        #expect(a.tabTitle == "acme-api")
        #expect(a.needsYou == 0)
        a.team?.receive(.decision(TeamDecision(id: "d1", from: "reviewer", kind: .askOwner, summary: "Merge #42?")))
        a.team?.receive(.decision(TeamDecision(id: "d2", from: "builder", kind: .pane, summary: "Open a pane?")))
        a.team?.receive(.decision(TeamDecision(id: "d3", from: "lead", kind: .askOwner, summary: "Old", state: .expired)))
        #expect(a.needsYou == 2)
        #expect(a.tabTitle == "acme-api \u{00B7} 2")
        #expect(b.needsYou == 0)
        #expect(b.tabTitle == "web")
        a.leave()
        b.leave()
    }

    // MARK: ⌃⌘1–⌃⌘9

    /// ⌃⌘n picks the nth tab of the frontmost tab group; past the last tab,
    /// nothing (as ⌘1–⌘9 do for terminal tabs).
    @Test func controlCommandNumberPicksTheNthWorkspaceTab() {
        let tabs = ["a", "b", "c"]
        #expect(WorkspaceTabKeys.tab(number: 1, in: tabs) == "a")
        #expect(WorkspaceTabKeys.tab(number: 2, in: tabs) == "b")
        #expect(WorkspaceTabKeys.tab(number: 3, in: tabs) == "c")
        #expect(WorkspaceTabKeys.tab(number: 4, in: tabs) == nil)
        #expect(WorkspaceTabKeys.tab(number: 9, in: tabs) == nil)
        #expect(WorkspaceTabKeys.tab(number: 0, in: tabs) == nil)
        #expect(WorkspaceTabKeys.numbers == Array(1...9))
        // ⌃⌘, so ⌘1–⌘9 stay the terminal's tabs (R-0345).
        #expect(WorkspaceTabKeys.modifiers == [.control, .command])
        #expect(WorkspaceTabKeys.shortcut(3) == "\u{2303}\u{2318}3")
    }

    /// A lone window is a group of one: ⌃⌘1 is that window.
    @Test func aLoneWindowIsItsOwnGroup() {
        let w = window()
        #expect(WorkspaceTabKeys.group(of: w).count == 1)
        #expect(WorkspaceTabKeys.group(of: w).first === w)
    }
}
