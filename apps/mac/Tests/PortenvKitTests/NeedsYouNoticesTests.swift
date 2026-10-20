// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// What the notifier was asked to do, in order.
@MainActor
final class FakePoster: NeedsYouPosting {
    var posted: [NeedsYouNotice] = []
    var withdrawn: [String] = []
    var badges: [String?] = []

    func post(_ n: NeedsYouNotice) { posted.append(n) }
    func withdraw(_ ids: [String]) { withdrawn += ids }
    func setBadge(_ label: String?) { badges.append(label) }
}

/// Notifications when an agent needs you, and the Dock badge (GUIDELINES
/// §5 and §6.2, basics audit rows 13 and 14, R-0384).
@MainActor
struct NeedsYouNoticesTests {
    let start = Date(timeIntervalSince1970: 1_000_000)
    let reviewer = TeamAgent(id: "reviewer", name: "Reviewer", status: .running)

    func decision(_ id: String, from: String = "reviewer", kind: TeamDecision.Kind = .askOwner,
                  summary: String = "Merge #42?", at offset: TimeInterval = 10,
                  choices: [String] = []) -> TeamDecision {
        TeamDecision(id: id, from: from, kind: kind, summary: summary, createdAt: start.addingTimeInterval(offset), choices: choices)
    }

    func team(_ decisions: [TeamDecision], agents: [TeamAgent]? = nil) -> TeamState {
        var s = TeamState()
        for a in agents ?? [reviewer] { s.apply(.agent(a)) }
        for d in decisions { s.apply(.decision(d)) }
        return s
    }

    func center(_ poster: FakePoster, front: String? = nil) -> NeedsYouCenter {
        NeedsYouCenter(poster: poster, inFront: { $0 == front }, now: { [start] in start })
    }

    // MARK: Content

    /// §5: "<workspace> · <who> is asking", the question in one line, and
    /// who asked and where.
    @Test func aDecisionReadsAsSection5Says() {
        let n = NeedsYouNotice.decision(decision("d1"), box: "acme-api", team: team([]), location: nil)
        #expect(n.content == NotifyText.Content(title: "acme-api \u{00B7} Reviewer is asking", body: "Merge #42?",
                                                subtitle: "Reviewer \u{00B7} This Mac"))
        #expect(n.id == "decision-acme-api-d1")
        #expect(n.route == .decision(box: "acme-api", id: "d1"))
        let remote = NeedsYouNotice.decision(decision("d1"), box: "web", team: team([]), location: "test-server")
        #expect(remote.content.subtitle == "Reviewer \u{00B7} test-server")
    }

    /// The team's first start is Portenv's own question; an agent that has
    /// left is named by its id.
    @Test func portenvsOwnQuestionAndAGoneAgent() {
        let first = NeedsYouNotice.decision(decision("d2", from: "portenv", kind: .teamStart, summary: "Start the team?"),
                                            box: "acme", team: team([]), location: nil)
        #expect(first.content.title == "acme \u{00B7} Portenv is asking")
        let gone = NeedsYouNotice.decision(decision("d3", from: "builder"), box: "acme", team: team([]), location: nil)
        #expect(gone.content.title == "acme \u{00B7} builder is asking")
    }

    /// A tab's program waiting for input (§5): the question, or for a
    /// password only that it asks, never its text.
    @Test func aWaitingTabReadsAsSection5Says() {
        let tab = TabInfo(id: "@3", name: "tests", active: false, program: "claude",
                          waiting: TabQuestion(text: "Run the migration?", secret: false))
        let n = NeedsYouNotice.waiting(tab, box: "acme", team: [], location: nil)
        #expect(n?.content == NotifyText.Content(title: "acme \u{00B7} Claude Code is asking", body: "Run the migration?",
                                                 subtitle: "In the tests tab \u{00B7} This Mac"))
        #expect(n?.id == "waiting-acme-@3" && n?.route == .tab(box: "acme", id: "@3"))
        let agentTab = TabInfo(id: "@4", name: "review", active: false, program: "claude",
                               waiting: TabQuestion(text: "ok?", secret: false), agentID: "reviewer")
        #expect(NeedsYouNotice.waiting(agentTab, box: "acme", team: [reviewer], location: "srv")?.content.title
            == "acme \u{00B7} Reviewer is asking")
        let secret = TabInfo(id: "@5", name: "shell", active: false, program: "sudo",
                             waiting: TabQuestion(text: "hunter2?", secret: true))
        let s = NeedsYouNotice.waiting(secret, box: "acme", team: [], location: nil)
        #expect(s?.content.body == NotifyText.secretPrompt)
        #expect(s?.content.body.contains("hunter2") == false)
        #expect(NeedsYouNotice.waiting(TabInfo(id: "@6", name: "x", active: false), box: "acme", team: [], location: nil) == nil)
    }

    /// What an agent wrote reaches the notification as one plain line:
    /// no escapes, no line breaks.
    @Test func agentTextIsOnePlainLine() {
        let d = decision("d1", summary: "Merge\u{1B}[31m now\nplease")
        let n = NeedsYouNotice.decision(d, box: "acme", team: team([]), location: nil)
        #expect(!n.content.body.contains("\u{1B}") && !n.content.body.contains("\n"))
        let tab = TabInfo(id: "@1", name: "t", active: false, program: "x",
                          waiting: TabQuestion(text: "a\u{202E}b\nc", secret: false))
        let w = NeedsYouNotice.waiting(tab, box: "acme", team: [], location: nil)
        #expect(w?.content.body.contains("\u{202E}") == false && w?.content.body.contains("\n") == false)
    }

    // MARK: Never Approve from a notification (§6.2, R-0384)

    /// Every decision kind, agent files and the team's first start
    /// included, offers Show only: no Approve, no Decline, no choices.
    @Test func aDecisionIsNeverAnsweredFromANotification() {
        let kinds: [TeamDecision.Kind] = [.askOwner, .pane, .agentFileChange, .shareLogin, .package, .teamStart, .deleteAgentFiles]
        for kind in kinds {
            let d = decision("d-\(kind)", kind: kind, choices: kind == .askOwner ? ["a", "b"] : [])
            let n = NeedsYouNotice.decision(d, box: "acme", team: team([]), location: nil)
            #expect(n.actions == [.show], "\(kind)")
            #expect(n.category == NeedsYouNotice.decisionCategory)
        }
        #expect(NotificationAction.allCases == [.show])
    }

    /// A click, or any action, on a decision's notification only shows it:
    /// the route the app gets has nothing to answer with.
    @Test func anyActionOnlyShows() {
        let info = NotificationRoute.decision(box: "acme", id: "d1").userInfo
        #expect(NotificationRoute(userInfo: info) == .decision(box: "acme", id: "d1"))
        #expect(NotificationRoute(userInfo: NotificationRoute.tab(box: "a", id: "@1").userInfo) == .tab(box: "a", id: "@1"))
        #expect(NotificationRoute(userInfo: ["kind": "decision", "workspace": "acme"]) == nil)
        #expect(NotificationRoute(userInfo: [:]) == nil)
        #expect(NotificationRoute(userInfo: ["kind": "approve", "workspace": "acme", "id": "d1"]) == nil)
    }

    // MARK: Routing

    /// Clicking opens what the sidebar's Needs-you row opens: the asking
    /// agent's column, or the decision on its own when Portenv asks.
    @Test func aClickSelectsTheDecision() {
        let s = team([decision("d1"), decision("d2", from: "portenv", kind: .teamStart)])
        #expect(NotificationRoute.selection(decision: "d1", in: s) == .agent("reviewer"))
        #expect(NotificationRoute.selection(decision: "d2", in: s) == .decision("d2"))
        // Answered or gone since: the team as it is.
        #expect(NotificationRoute.selection(decision: "gone", in: s) == nil)
    }

    // MARK: When

    /// One notification per new decision, as it arrives; nothing again
    /// for one already posted, and an answered one is taken back.
    @Test func newDecisionsPostOnceAndAnsweredOnesAreWithdrawn() {
        let poster = FakePoster()
        let c = center(poster)
        c.team(box: "acme", state: team([]), location: nil)
        c.team(box: "acme", state: team([decision("d1")]), location: nil)
        c.team(box: "acme", state: team([decision("d1")]), location: nil)
        #expect(poster.posted.map(\.id) == ["decision-acme-d1"])
        var answered = decision("d1")
        answered.state = .approved
        c.team(box: "acme", state: team([answered]), location: nil)
        #expect(poster.withdrawn == ["decision-acme-d1"])
    }

    /// Decisions already waiting when the app started following the team
    /// don't notify (the badge counts them); new ones do.
    @Test func onlyDecisionsAskedSinceFollowingNotify() {
        let poster = FakePoster()
        let c = center(poster)
        c.team(box: "acme", state: team([decision("old", at: -600)]), location: nil)
        #expect(poster.posted.isEmpty)
        c.team(box: "acme", state: team([decision("old", at: -600), decision("new", at: 5)]), location: nil)
        #expect(poster.posted.map(\.id) == ["decision-acme-new"])
        #expect(poster.badges.last == "2")
    }

    /// §5: no notification for the workspace whose window is in front (it
    /// shows the decision); another workspace's still notifies.
    @Test func theFrontWorkspaceIsNotNotified() {
        let poster = FakePoster()
        let c = center(poster, front: "acme")
        c.team(box: "acme", state: team([decision("d1")]), location: nil)
        c.team(box: "web", state: team([decision("d2")]), location: nil)
        #expect(poster.posted.map(\.id) == ["decision-web-d2"])
        // Seen in front: it doesn't post later either.
        c.team(box: "acme", state: team([decision("d1")]), location: nil)
        #expect(poster.posted.count == 1)
    }

    /// A tab's question notifies while the window isn't in front, a new
    /// question replaces it under the same id, and it's withdrawn once
    /// answered.
    @Test func waitingTabsNotifyReplaceAndWithdraw() {
        let poster = FakePoster()
        let c = center(poster, front: "front")
        func tabs(_ q: String?) -> [TabInfo] {
            [TabInfo(id: "@1", name: "t", active: true, program: "claude", waiting: q.map { TabQuestion(text: $0, secret: false) })]
        }
        c.tabs(box: "acme", tabs: tabs(nil), team: [], location: nil)
        c.tabs(box: "acme", tabs: tabs("A?"), team: [], location: nil)
        c.tabs(box: "acme", tabs: tabs("A?"), team: [], location: nil)
        c.tabs(box: "acme", tabs: tabs("B?"), team: [], location: nil)
        #expect(poster.posted.map(\.content.body) == ["A?", "B?"])
        #expect(Set(poster.posted.map(\.id)) == ["waiting-acme-@1"])
        c.tabs(box: "acme", tabs: tabs(nil), team: [], location: nil)
        #expect(poster.withdrawn == ["waiting-acme-@1"])
        c.tabs(box: "front", tabs: tabs("C?"), team: [], location: nil)
        #expect(poster.posted.count == 2)
    }

    // MARK: Dock badge

    /// The badge is every open workspace's Needs you, added up; none when
    /// nothing waits.
    @Test func theBadgeCountsEverySession() {
        let poster = FakePoster()
        let c = center(poster)
        c.team(box: "acme", state: team([decision("a1"), decision("a2")]), location: nil)
        c.team(box: "web", state: team([decision("w1")]), location: nil)
        #expect(c.count == 3 && poster.badges.last == "3")
        var done = decision("a1")
        done.state = .declined
        c.team(box: "acme", state: team([done, decision("a2")]), location: nil)
        #expect(poster.badges.last == "2")
        // A window closes: its workspace keeps running, but no session
        // follows it, so it leaves the count and its notifications go.
        c.closed(box: "web")
        #expect(poster.badges.last == "1" && poster.withdrawn.contains("decision-web-w1"))
        var gone = decision("a2")
        gone.state = .expired
        c.team(box: "acme", state: team([done, gone]), location: nil)
        #expect(c.count == 0 && poster.badges.last == .some(nil))
        // Unchanged counts don't touch the badge again.
        let before = poster.badges.count
        c.team(box: "acme", state: team([done, gone]), location: nil)
        #expect(poster.badges.count == before)
    }

    @Test func badgeLabel() {
        #expect(DockBadge.label(0) == nil)
        #expect(DockBadge.label(1) == "1")
        #expect(DockBadge.label(42) == "42")
    }

    // MARK: The team's hook

    /// The session's TeamController says when its team changes, whether
    /// the change came from the stream or an answer given here.
    @Test func theTeamControllerReportsChanges() async {
        let daemon = FakeDaemon()
        let pending = decision("d1")
        daemon.teamScripts = [.init(events: [(.agent(reviewer), "c1"), (.decision(pending), "c2")], failure: nil)]
        var approved = pending
        approved.state = .approved
        daemon.answered = ["d1": approved]
        let team = TeamController(box: "acme", daemon: daemon, retry: .milliseconds(1))
        var seen: [Int] = []
        team.changed = { seen.append($0.needsYou.count) }
        let follow = Task { await team.follow() }
        for _ in 0 ..< 200 where team.state.cursor != "c2" { try? await Task.sleep(for: .milliseconds(5)) }
        follow.cancel()
        await team.answer(pending, approve: true)
        #expect(seen.last == 0 && seen.contains(1))
    }

    /// The tabs controller says when its tabs change.
    @Test func theTabsControllerReportsChanges() {
        let tabs = TabsController(box: "acme", daemon: FakeDaemon())
        var seen: [[String]] = []
        tabs.changed = { seen.append($0.map(\.id)) }
        tabs.receive([TabInfo(id: "@1", name: "a", active: true)])
        #expect(seen == [["@1"]])
    }
}
