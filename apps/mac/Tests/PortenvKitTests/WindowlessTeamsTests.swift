// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Workspaces kept running with no window (ADR 0023) still notify and count
/// in the Dock badge: the app follows their teams itself, with the same
/// WatchTeam a window uses (R-0388 follow-up).
@MainActor
struct WindowlessTeamsTests {
    let start = Date(timeIntervalSince1970: 2_000_000)
    let builder = TeamAgent(id: "builder", name: "Builder", status: .running)

    func ask(_ id: String, from: String = "builder", at offset: TimeInterval = 10) -> TeamDecision {
        TeamDecision(id: id, from: from, kind: .askOwner, summary: "Merge?", createdAt: start.addingTimeInterval(offset))
    }

    func until(_ done: () -> Bool) async {
        for _ in 0 ..< 400 where !done() { try? await Task.sleep(for: .milliseconds(5)) }
    }

    func setup(_ daemon: FakeDaemon, shown: Set<String> = []) -> (WindowlessTeams, FakePoster, NeedsYouCenter) {
        let poster = FakePoster()
        let center = NeedsYouCenter(poster: poster, inFront: { _ in false }, now: { [start] in start })
        let teams = WindowlessTeams(daemon: daemon, center: center, retry: .milliseconds(1))
        teams.shown = { shown }
        return (teams, poster, center)
    }

    /// A running workspace no window shows is followed: its new decision
    /// notifies, as "<workspace> · <who> is asking", and counts in the badge.
    @Test func aWindowlessWorkspaceNotifiesAndCounts() async {
        let daemon = FakeDaemon()
        daemon.teamScriptsByBox = ["acme": [.init(events: [(.agent(builder), "1"), (.decision(ask("d1")), "2")], failure: nil)]]
        let (teams, poster, center) = setup(daemon)
        teams.update(running: ["acme"])
        await until { center.count == 1 }
        #expect(teams.following == ["acme"])
        #expect(poster.posted.map(\.content.title) == ["acme \u{00B7} Builder is asking"])
        #expect(poster.posted.first?.route == .decision(box: "acme", id: "d1"))
        #expect(poster.badges.last == "1")
        teams.stopAll()
    }

    /// Workspaces a window shows are the window's to follow; one that
    /// stops running leaves the badge and its notifications go.
    @Test func onlyRunningWorkspacesWithoutAWindow() async {
        let daemon = FakeDaemon()
        daemon.teamScriptsByBox = ["web": [.init(events: [(.decision(ask("w1")), "1")], failure: nil)]]
        let (teams, poster, center) = setup(daemon, shown: ["acme"])
        teams.update(running: ["acme", "web"])
        #expect(teams.following == ["web"])
        #expect(!daemon.calls.contains(["app", "watch-team", "acme", ""]))
        await until { center.count == 1 }
        teams.update(running: ["acme"])
        #expect(teams.following.isEmpty)
        #expect(center.count == 0 && poster.withdrawn == ["decision-web-w1"])
    }

    /// A window opening the workspace takes over without a gap: the
    /// background follow stops and the count stays until the window's
    /// team reports. Its window closing hands it back.
    @Test func aWindowTakesOverAndHandsBack() async {
        let daemon = FakeDaemon()
        daemon.teamScriptsByBox = ["acme": [.init(events: [(.decision(ask("d1")), "1")], failure: nil)]]
        let (teams, _, center) = setup(daemon)
        teams.update(running: ["acme"])
        await until { center.count == 1 }
        teams.shown = { ["acme"] }
        teams.windowOpened("acme")
        #expect(teams.following.isEmpty && center.count == 1)
        // Running, but a window shows it: not followed again here.
        teams.update(running: ["acme"])
        #expect(teams.following.isEmpty)
        // The app takes the window's count back first (NeedsYouCenter.closed).
        center.closed(box: "acme")
        daemon.teamScriptsByBox = ["acme": [.init(events: [(.decision(ask("d1")), "1")], failure: nil)]]
        teams.shown = { [] }
        teams.windowClosed("acme", running: true)
        #expect(teams.following == ["acme"])
        await until { center.count == 1 }
        #expect(center.count == 1)
        // Followed afresh, from the start of the team.
        #expect(daemon.calls.filter { $0 == ["app", "watch-team", "acme", ""] }.count == 2)
        teams.stopAll()
    }

    /// Show on a windowless workspace's notification: its team (followed
    /// here) says what to select, as the Needs-you row would.
    @Test func showKnowsWhatToSelect() async {
        let daemon = FakeDaemon()
        daemon.teamScriptsByBox = ["acme": [.init(events: [(.agent(builder), "1"), (.decision(ask("d1")), "2")], failure: nil)]]
        let (teams, _, center) = setup(daemon)
        teams.update(running: ["acme"])
        await until { center.count == 1 }
        #expect(teams.selection(for: .decision(box: "acme", id: "d1")) == .agent("builder"))
        #expect(teams.selection(for: .decision(box: "other", id: "d1")) == nil)
        teams.stopAll()
    }

    /// A window opened from a notification selects the decision once its
    /// team has it, however late it arrives.
    @Test func aWindowSelectsTheDecisionWhenItArrives() async {
        let daemon = FakeDaemon()
        daemon.teamScripts = [.init(events: [(.agent(builder), "1"), (.decision(ask("d1")), "2")], failure: nil)]
        let team = TeamController(box: "acme", daemon: daemon, retry: .milliseconds(1))
        team.show(decision: "d1")
        #expect(team.selection == .allTabs)
        let follow = Task { await team.follow() }
        await until { team.selection != .allTabs }
        follow.cancel()
        #expect(team.selection == .agent("builder"))
        // Once only: the owner's own choice afterwards stands.
        team.selection = .you
        team.receive(.decision(ask("d2")))
        #expect(team.selection == .you)
    }
}
