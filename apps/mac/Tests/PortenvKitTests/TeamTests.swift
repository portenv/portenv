// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The team as the sidebar shows it (ADR 0022, R-0240, R-0241): statuses in
/// words and symbols, agents' text as plain text, the stream folded into
/// one state, and the owner's actions through portenvd.
struct TeamTextTests {
    @Test func statusesAreWordsAndSymbols() {
        #expect(TeamText.word(.running) == "running" && TeamText.symbol(.running) == "play.fill")
        #expect(TeamText.word(.idle) == "idle" && TeamText.symbol(.idle) == "circle.dotted")
        #expect(TeamText.word(.needsYou) == "needs you" && TeamText.symbol(.needsYou) == "circle.fill")
        #expect(TeamText.word(.invalid) == "can\u{2019}t start")
        #expect(TeamText.word(.stopped) == "stopped" && TeamText.symbol(.stopped) == "stop.fill")
    }

    /// Board text is data: no escape sequences, control characters or bidi
    /// overrides reach a view; line breaks stay in messages, never in a row.
    @Test func agentTextIsPlain() {
        let planted = "ok\u{1b}]8;;file:///etc\u{07}link\u{1b}[31m red\u{9b}2J\u{202e}gnp.exe\u{7f}\ttab\nnext"
        let shown = TeamText.plain(planted, max: 1000)
        #expect(!shown.unicodeScalars.contains { $0.value < 0x20 && $0 != "\n" })
        #expect(!shown.unicodeScalars.contains { (0x7F ... 0x9F).contains($0.value) || $0.value == 0x202E })
        #expect(shown == "ok]8;;file:///etclink[31m red2Jgnp.exe tab\nnext")
        #expect(TeamText.line("two\nlines   and  spaces", max: 100) == "two lines and spaces")
        #expect(TeamText.line(String(repeating: "x", count: 500), max: 200).count == 200)
        let m = TeamMessage(id: "1", from: "builder", to: "owner", text: String(repeating: "y", count: 9000))
        #expect(m.text.count == 8 * 1024)
        let a = TeamAgent(id: "builder", name: "Builder\n\u{1b}[2J", status: .running, lastLine: "Tests\u{0}pass\nnow")
        #expect(a.name == "Builder [2J" && a.lastLine == "Testspass now")
    }

    @Test func rowsAndColumnsReadAsTheMockupsShow() {
        let reviewer = TeamAgent(id: "reviewer", name: "Reviewer", status: .needsYou, lastLine: "Wants to merge #42")
        #expect(TeamText.spoken(reviewer) == "Reviewer, needs you. Wants to merge #42")
        let invalid = TeamAgent(id: "designer", name: "designer.md", status: .invalid, problem: "Unknown key \u{201C}grants\u{201D} on line 6")
        #expect(TeamText.spoken(invalid) == "designer.md, can\u{2019}t start. Unknown key \u{201C}grants\u{201D} on line 6")
        let builder = TeamAgent(id: "builder", name: "Builder", status: .running, runner: "/usr/local/bin/claude --continue",
                                grant: TeamGrant(panes: .ask, maxPanes: 4))
        #expect(TeamText.runsAs(builder) == "Runs as user builder \u{00B7} Claude Code \u{00B7} may open 4 panes")
        var lead = builder
        lead.id = "lead"
        lead.runner = "my-agent --x"
        lead.grant.panes = .never
        #expect(TeamText.runsAs(lead) == "Runs as user lead \u{00B7} my-agent \u{00B7} can\u{2019}t open panes")
        #expect(TeamText.needsYouHeading(1) == "Needs you \u{00B7} 1")
        let asked = Date(timeIntervalSince1970: 1000)
        #expect(TeamText.waiting(since: asked, now: asked.addingTimeInterval(6 * 60 + 5)) == "Waiting 6 min")
        #expect(TeamText.waiting(since: asked, now: asked.addingTimeInterval(20)) == "Waiting less than a minute")
        #expect(TeamText.waiting(since: asked, now: asked.addingTimeInterval(3 * 3600)) == "Waiting 3 hours")
        #expect(TeamText.quota(used: 212_000_000, of: 500_000_000, locale: Locale(identifier: "en")) == "212 MB of 500 MB")
        let en = Locale(identifier: "en")
        #expect(TeamText.size(1_200_000, locale: en) == "1.2 MB" && TeamText.size(4_096, locale: en) == "4.1 KB")
        #expect(TeamText.size(2_000_000_000, locale: en) == "2 GB" && TeamText.size(512, locale: en) == "512 bytes")
    }

    @Test func coloursFollowTeamOrderUnlessTheFileNamesOne() {
        let team = ["lead", "builder", "reviewer", "keeper", "five", "six", "seven"].map { TeamAgent(id: $0, name: $0, status: .idle) }
        #expect(team.map { AgentColours.name(for: $0, in: team) } == ["indigo", "teal", "purple", "pink", "brown", "mint", "indigo"])
        var named = team[0]
        named.avatar = "mint"
        #expect(AgentColours.name(for: named, in: team) == "mint")
    }
}

struct TeamStateTests {
    func at(_ s: Double) -> Date { Date(timeIntervalSince1970: s) }

    @Test func eventsFoldIntoOneTeam() {
        var t = TeamState()
        t.apply(.agent(TeamAgent(id: "lead", name: "Lead", status: .idle)), cursor: "c1")
        t.apply(.agent(TeamAgent(id: "builder", name: "Builder", status: .starting)), cursor: "c2")
        t.apply(.agent(TeamAgent(id: "builder", name: "Builder", status: .running)), cursor: "c3")
        #expect(t.agents.map(\.id) == ["lead", "builder"])
        #expect(t.agent("builder")?.status == .running)
        #expect(t.cursor == "c3" && t.hasTeam)
        // A message seen again after a resume is kept once.
        let m = TeamMessage(id: "m1", from: "builder", to: "owner", text: "Split done.")
        t.apply(.message(m), cursor: "c4")
        t.apply(.message(m))
        #expect(t.messages.count == 1 && t.cursor == "c4")
        t.apply(.removed("lead"))
        #expect(t.agents.map(\.id) == ["builder"])
    }

    @Test func needsYouIsPendingDecisionsOldestFirst() {
        var t = TeamState()
        t.apply(.decision(TeamDecision(id: "d2", from: "reviewer", kind: .askOwner, summary: "Merge #42?", createdAt: at(200))))
        t.apply(.decision(TeamDecision(id: "d1", from: "builder", kind: .pane, summary: "Open a pane?", createdAt: at(100))))
        t.apply(.decision(TeamDecision(id: "d3", from: "lead", kind: .askOwner, summary: "Old", state: .expired, createdAt: at(50))))
        #expect(t.needsYou.map(\.id) == ["d1", "d2"])
        // The answer arrives as the same decision at a new state.
        t.apply(.decision(TeamDecision(id: "d1", from: "builder", kind: .pane, summary: "Open a pane?", state: .approved, createdAt: at(100))))
        #expect(t.needsYou.map(\.id) == ["d2"])
        #expect(t.decisions.count == 3)
        // Expired is final: never answerable.
        #expect(!t.decisions.first { $0.id == "d3" }!.answerable)
    }

    @Test func aConversationIsOnlyBetweenTheOwnerAndThatAgent() {
        var t = TeamState()
        for (id, from, to) in [("1", "lead", "builder"), ("2", "builder", "owner"), ("3", "owner", "builder"), ("4", "owner", "reviewer"), ("5", "portenv", "owner")] {
            t.apply(.message(TeamMessage(id: id, from: from, to: to, text: id)))
        }
        #expect(t.conversation(with: "builder").map(\.id) == ["2", "3"])
    }
}

@MainActor
struct TeamControllerTests {
    let builder = TeamAgent(id: "builder", name: "Builder", status: .running)

    /// The stream resumes from its cursor after it ends, and keeps what it
    /// had.
    @Test func followResumesFromTheCursor() async {
        let daemon = FakeDaemon()
        daemon.teamScripts = [
            .init(events: [(.agent(builder), "c1"), (.message(TeamMessage(id: "m1", from: "builder", to: "owner", text: "hi")), "c2")], failure: "portenvd went away"),
            .init(events: [(.message(TeamMessage(id: "m1", from: "builder", to: "owner", text: "hi")), "c2"),
                           (.decision(TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "Merge?")), "c3")], failure: nil),
        ]
        let team = TeamController(box: "acme", daemon: daemon, retry: .milliseconds(1))
        let follow = Task { await team.follow() }
        for _ in 0 ..< 200 where team.state.cursor != "c3" { try? await Task.sleep(for: .milliseconds(5)) }
        follow.cancel()
        #expect(daemon.calls.filter { $0[1] == "watch-team" }.prefix(2).map { $0[3] } == ["", "c2"])
        #expect(team.state.messages.count == 1 && team.state.needsYou.map(\.id) == ["d1"])
        #expect(team.supported)
    }

    /// An older portenvd: no team, no retrying, the sidebar stays hidden.
    @Test func anOlderPortenvdHasNoTeam() async {
        let daemon = FakeDaemon()
        daemon.teamScripts = [.init(events: [], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon, retry: .milliseconds(1))
        await team.follow()
        #expect(!team.supported && !team.state.hasTeam)
        #expect(daemon.calls.filter { $0[1] == "watch-team" }.count == 1)
    }

    /// Decisions are answered only while pending; the answer comes back as
    /// the decision's new state.
    @Test func answeringADecision() async {
        let daemon = FakeDaemon()
        let pending = TeamDecision(id: "d1", from: "reviewer", kind: .askOwner, summary: "Merge #42?")
        var approved = pending
        approved.state = .approved
        daemon.answered = ["d1": approved]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.answer(pending, approve: true)
        #expect(daemon.calls.last == ["app", "answer-decision", "acme", "d1", "approve", ""])
        #expect(team.state.decisions.first?.state == .approved && team.state.needsYou.isEmpty)
        var expired = pending
        expired.state = .expired
        await team.answer(expired, approve: true)
        #expect(daemon.calls.filter { $0[1] == "answer-decision" }.count == 1)
    }

    @Test func messagesAndStartStop() async {
        let daemon = FakeDaemon()
        let team = TeamController(box: "acme", daemon: daemon)
        await team.send("   ", to: "builder")
        await team.send(" Ping me first. ", to: "builder")
        #expect(daemon.calls.filter { $0[1] == "send-message" } == [["app", "send-message", "acme", "builder", "Ping me first."]])
        #expect(team.state.conversation(with: "builder").map(\.text) == ["Ping me first."])
        await team.start("builder")
        #expect(team.state.agent("builder")?.status == .starting)
        await team.stop("builder")
        #expect(team.state.agent("builder")?.status == .stopped)
        daemon.failing["stop-agent"] = "builder isn\u{2019}t running."
        await team.stop("builder")
        #expect(team.error == "builder isn\u{2019}t running.")
    }

    @Test func removingTheSelectedAgentShowsAllTabs() async {
        let daemon = FakeDaemon()
        daemon.teamScripts = [.init(events: [(.agent(builder), "c1"), (.removed("builder"), "c2")], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        team.selection = .agent("builder")
        await team.follow()
        #expect(team.selection == .allTabs && team.selectedAgent == nil)
    }
}
