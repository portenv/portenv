// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
import Testing

@testable import PortenvKit

/// R-0412 item 18: an agent's runner waiting at its permission prompt
/// ("Do you want to proceed?") is a Needs-you item, with Show, until it's
/// answered in its terminal, it goes idle or it stops.
@MainActor
struct PermissionWaitTests {
    let start = Date(timeIntervalSince1970: 1_000_000)

    func builder(_ summary: String?, at offset: TimeInterval = 0) -> TeamAgent {
        TeamAgent(id: "builder", name: "Builder", status: summary == nil ? .running : .needsYou,
                  tabIDs: ["builder:@1", "builder:@2"],
                  permission: summary.map { PermissionPrompt(summary: $0, since: start.addingTimeInterval(offset), tabID: "builder:@1") })
    }

    @Test func itReadsAsTheItemSays() {
        let a = builder("npm install left-pad")
        #expect(TeamText.waitingForPermission(a) == "Builder is waiting for your permission: npm install left-pad")
        // Whatever arrives, the line is plain, one line and short.
        let p = PermissionPrompt(summary: "rm -rf x\n\u{1b}[2J\u{202e}" + String(repeating: "z", count: 300), since: nil, tabID: "t")
        #expect(p.summary.count <= 80 && !p.summary.contains("\n") && !p.summary.contains("\u{1b}") && !p.summary.contains("\u{202e}"))
    }

    @Test func theAppReadsItFromPortenvd() {
        var info = Portenv_Daemon_V1_AgentInfo()
        info.id = "builder"
        info.name = "Builder"
        info.state = .needsYou
        info.permission.summary = "git push"
        info.permission.tabID = "builder:@1"
        info.permission.since = .init(date: start)
        let a = TeamAgent(info)
        #expect(a.permission == PermissionPrompt(summary: "git push", since: start, tabID: "builder:@1"))
        info.clearPermission()
        #expect(TeamAgent(info).permission == nil)
    }

    /// Needs you counts it with the decisions: the sidebar's heading, the
    /// window tab's count; it goes when portenvd's row says it's over.
    @Test func needsYouCountsIt() {
        var t = TeamState()
        t.apply(.agent(builder("git push")))
        t.apply(.decision(TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "Merge?")))
        #expect(t.permissionWaits.map(\.id) == ["builder"])
        #expect(t.needsYouCount == 2)
        t.apply(.agent(builder(nil)))
        #expect(t.permissionWaits.isEmpty && t.needsYouCount == 1)
    }

    /// Show opens the agent's runner tab, while it's still a tab of that
    /// agent's; nothing for an agent that isn't waiting.
    @Test func showOpensTheAgentsTab() {
        let tabs = [TabInfo(id: "@1", name: "shell", active: true),
                    TabInfo(id: "builder:@1", name: "main session", active: false, agentID: "builder"),
                    TabInfo(id: "builder:@2", name: "tests", active: false, agentID: "builder")]
        #expect(PermissionShow.tab(for: builder("ls"), in: tabs) == "builder:@1")
        #expect(PermissionShow.tab(for: builder(nil), in: tabs) == nil)
        // A tab id that isn't the agent's own is never opened.
        var other = builder("ls")
        other.permission?.tabID = "@1"
        #expect(PermissionShow.tab(for: other, in: tabs) == nil)
        #expect(PermissionShow.tab(for: builder("ls"), in: tabs.filter { $0.id != "builder:@1" }) == nil)
    }

    /// Showing it selects the agent and asks the terminal for the keys.
    @Test func showingSelectsTheAgentAndFocusesTheTerminal() {
        let team = TeamController(box: "acme-api", daemon: FakeDaemon())
        team.receive(.agent(builder("git push")))
        let before = team.terminalFocusRequests
        team.showPermission(agent: "builder")
        #expect(team.selection == .agent("builder") && team.terminalFocusRequests == before + 1)
    }

    // MARK: Notifications

    func center(_ poster: FakePoster, front: String? = nil) -> NeedsYouCenter {
        NeedsYouCenter(poster: poster, inFront: { $0 == front }, now: { [start] in start })
    }

    func team(_ agents: [TeamAgent]) -> TeamState {
        var s = TeamState()
        for a in agents { s.apply(.agent(a)) }
        return s
    }

    @Test func aWaitNotifiesWithShowAndIsTakenBackWhenOver() {
        let poster = FakePoster()
        let c = center(poster)
        c.team(box: "acme-api", state: team([builder(nil)]), location: nil)
        c.team(box: "acme-api", state: team([builder("git push", at: 30)]), location: nil)
        #expect(poster.posted.count == 1)
        let n = poster.posted[0]
        #expect(n.id == "permission-acme-api-builder")
        #expect(n.content == NotifyText.Content(title: "acme-api \u{00B7} Builder is asking",
                                                body: "Builder is waiting for your permission: git push",
                                                subtitle: "Builder \u{00B7} This Mac"))
        #expect(n.route == .agent(box: "acme-api", id: "builder") && n.actions == [.show])
        #expect(c.count == 1 && poster.badges.last == "1")
        // The same wait again: nothing new. A new prompt replaces it.
        c.team(box: "acme-api", state: team([builder("git push", at: 30)]), location: nil)
        #expect(poster.posted.count == 1)
        c.team(box: "acme-api", state: team([builder("npm test", at: 40)]), location: nil)
        #expect(poster.posted.count == 2 && poster.posted[1].id == "permission-acme-api-builder")
        // Over: taken back, and out of the badge.
        c.team(box: "acme-api", state: team([builder(nil)]), location: nil)
        #expect(poster.withdrawn == ["permission-acme-api-builder"])
        #expect(c.count == 0 && poster.badges.last == .some(nil))
    }

    @Test func aWaitInTheWindowInFrontCountsButDoesntNotify() {
        let poster = FakePoster()
        let c = center(poster, front: "acme-api")
        c.team(box: "acme-api", state: team([builder("git push")]), location: nil)
        #expect(poster.posted.isEmpty && c.count == 1)
    }

    @Test func theRouteSurvivesTheNotification() {
        let r = NotificationRoute.agent(box: "acme-api", id: "builder")
        #expect(NotificationRoute(userInfo: r.userInfo) == r)
        #expect(r.box == "acme-api")
    }
}
