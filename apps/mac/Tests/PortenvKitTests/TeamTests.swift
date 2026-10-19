// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
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

    /// Portenv's routine notes, status requests and hand-offs show as
    /// notices until the sidebar has kinds of its own for them.
    @Test func routineMessagesShowAsNotices() {
        var m = Portenv_Types_V1_BoardMessage()
        m.id = "r1"
        m.from = "portenv"
        m.to = "lead"
        m.type = .routine
        m.text = "Check the build"
        #expect(TeamMessage(m).kind == .notice)
    }

    /// A notice about one agent keeps whom it's about (R-0302).
    @Test func aNoticeKeepsWhoItsAbout() {
        var m = Portenv_Types_V1_BoardMessage()
        m.id = "n1"
        m.from = "portenv"
        m.to = "owner"
        m.type = .notice
        m.text = "Lead couldn\u{2019}t start: an old toolbox"
        m.about = "lead"
        #expect(TeamMessage(m).about == "lead")
    }

    @Test func coloursFollowTeamOrderUnlessTheFileNamesOne() {
        let team = ["lead", "builder", "reviewer", "keeper", "five", "six", "seven"].map { TeamAgent(id: $0, name: $0, status: .idle) }
        #expect(team.map { AgentColours.name(for: $0, in: team) } == ["indigo", "teal", "purple", "pink", "brown", "mint", "indigo"])
        var named = team[0]
        named.avatar = "mint"
        #expect(AgentColours.name(for: named, in: team) == "mint")
    }
}

/// No agent's avatar can look like the owner's (R-0253): not with a person
/// symbol, not with grey, not with the owner's initial in the owner's place.
struct AvatarLookTests {
    let anySymbol: (String) -> Bool = { !$0.isEmpty }

    @Test func noAgentLooksLikeTheOwner() {
        let attempts = ["", "person.fill", "person", "person.circle.fill", "person.crop.circle", "gray", "grey", "indigo",
                        "hammer.fill", "R", "\u{1b}[31m"]
        var team: [TeamAgent] = []
        for (i, avatar) in attempts.enumerated() {
            team.append(TeamAgent(id: "a\(i)", name: i % 2 == 0 ? "Reviewer" : "retnan", avatar: avatar, status: .idle))
        }
        for a in team {
            let look = AvatarLook.agent(a, in: team, isSymbol: anySymbol)
            #expect(look != AvatarLook.owner, "\(a.avatar)")
            #expect(look.colour != AvatarLook.owner.colour, "\(a.avatar)")
            #expect(!(look.symbol ?? "").hasPrefix("person"), "\(a.avatar)")
            #expect(AgentColours.order.contains(look.colour))
        }
        // A real symbol that isn't a person is kept; a colour name is a colour.
        #expect(AvatarLook.agent(team[8], in: team, isSymbol: anySymbol).symbol == "hammer.fill")
        #expect(AvatarLook.agent(team[7], in: team, isSymbol: anySymbol) == AvatarLook(symbol: nil, initial: "R", colour: "indigo"))
        #expect(AvatarLook.owner.symbol == "person.fill" && AvatarLook.owner.initial.isEmpty)
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
        // Portenv's notice to the owner about builder (a start that
        // failed) is in builder's column; one about lead isn't (R-0302).
        t.apply(.message(TeamMessage(id: "6", from: "portenv", to: "owner", kind: .notice, text: "6", about: "builder")))
        t.apply(.message(TeamMessage(id: "7", from: "portenv", to: "owner", kind: .notice, text: "7", about: "lead")))
        #expect(t.conversation(with: "builder").map(\.id) == ["2", "3", "6"])
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

    /// Why a start Approve set going failed shows in the row and under
    /// Start until a start works (R-0302).
    @Test func aFailedStartShowsUntilAStartWorks() async {
        let daemon = FakeDaemon()
        let failed = TeamAgent(id: "builder", name: "Builder", status: .stopped, problem: "Couldn\u{2019}t start: an old toolbox", lastLine: "Board line")
        daemon.teamScripts = [.init(events: [(.agent(failed), "c1")], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        let shown = team.state.agent("builder")!
        #expect(TeamText.startFailure(shown) == "Couldn\u{2019}t start: an old toolbox")
        #expect(TeamText.rowLine(shown, now: Date()).text == "Couldn\u{2019}t start: an old toolbox")
        #expect(team.canStart(shown))
        await team.start("builder")
        #expect(team.state.agent("builder").map(TeamText.startFailure) == .some(nil))
        #expect(team.state.agent("builder").map { TeamText.rowLine($0, now: Date()).text } != "Couldn\u{2019}t start: an old toolbox")
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

@MainActor
struct TeamTabsTests {
    /// The tab bar shows what the sidebar selects: every tab, yours, or one
    /// agent's (R-0241).
    @Test func tabsFollowTheSelection() async {
        let daemon = FakeDaemon()
        let tabs = TabsController(box: "acme", daemon: daemon)
        daemon.setTabs([
            TabInfo(id: "@1", name: "shell", active: true),
            TabInfo(id: "builder:@1", name: "main session", active: false, agentID: "builder", paneIDs: ["%1", "%4"]),
            TabInfo(id: "reviewer:@1", name: "review", active: false, agentID: "reviewer"),
        ])
        await tabs.load()
        #expect(tabs.shown(for: .allTabs).map(\.id) == ["@1", "builder:@1", "reviewer:@1"])
        #expect(tabs.shown(for: .you).map(\.id) == ["@1"])
        #expect(tabs.shown(for: .agent("builder")).map(\.paneIDs) == [["%1", "%4"]])
        #expect(tabs.shown(for: .agent("nobody")).isEmpty)
    }
}

/// The agent's status in its row (R-0256) and its routines in the menu
/// (R-0257, R-0258).
struct TeamStatusTextTests {
    let now = Date(timeIntervalSince1970: 100_000)
    func ago(_ minutes: Double) -> Date { now.addingTimeInterval(-minutes * 60) }

    @Test func aFreshStatusShowsWithItsAge() {
        let s = AgentWorkStatus(text: "Running the payments tests", setAt: ago(4))
        #expect(TeamStatusText.line(s, since: nil, now: now) == ("Running the payments tests \u{00B7} 4 min ago", false))
        let p = AgentWorkStatus(text: "Migrating tables", progress: (3, 5), setAt: ago(0.2))
        #expect(TeamStatusText.line(p, since: nil, now: now).text == "Migrating tables (3/5) \u{00B7} just now")
        let b = AgentWorkStatus(text: "Deploying", progress: (1, 2), blocked: "needs the staging key", setAt: ago(2))
        #expect(TeamStatusText.line(b, since: nil, now: now).text == "blocked: needs the staging key \u{00B7} 2 min ago")
    }

    @Test func noStatusOrAStaleOneSaysHowLong() {
        #expect(TeamStatusText.line(AgentWorkStatus(text: "Old", setAt: ago(35)), since: nil, now: now) == ("no status for 35 min", true))
        #expect(TeamStatusText.line(AgentWorkStatus(text: "Edge", setAt: ago(30)), since: nil, now: now).stale == false)
        #expect(TeamStatusText.line(nil, since: ago(95), now: now) == ("no status for 1 hour 35 min", true))
        #expect(TeamStatusText.line(nil, since: nil, now: now) == ("no status", true))
        // A cleared status (portenv status clear) counts as none.
        #expect(TeamStatusText.line(AgentWorkStatus(text: "", setAt: ago(5)), since: nil, now: now).stale)
    }

    @Test func statusIsPlainAndBounded() {
        let s = AgentWorkStatus(text: "line\n\u{1b}[31mred " + String(repeating: "x", count: 300), progress: (9, 5), blocked: "")
        #expect(!s.text.contains("\n") && !s.text.contains("\u{1b}") && s.text.count == 200)
        #expect(s.progress?.done == 5 && s.progress?.total == 5)
        #expect(AgentWorkStatus(text: "x", progress: (1, 0)).progress == nil)
    }

    @Test func routinesInTheMenu() {
        #expect(TeamStatusText.routines([]) == nil)
        #expect(TeamStatusText.routines([AgentRoutine(every: 30 * 60, note: "Status round")]) == "Routines: every 30 min")
        #expect(TeamStatusText.routines([AgentRoutine(every: 30 * 60, note: "a"), AgentRoutine(every: 2 * 3600, note: "b")])
            == "Routines: every 30 min, every 2 hours")
        #expect(TeamStatusText.routines([AgentRoutine(every: 3600, note: "a", paused: true)]) == "Routines: every 1 hour (paused)")
        #expect(TeamStatusText.pauseRoutines(false) == "Pause Routines" && TeamStatusText.pauseRoutines(true) == "Resume Routines")
        #expect(TeamStatusText.span(90 * 60) == "1 hour 30 min")
    }
}

/// An agent's tab can never look like the owner's (R-0265 item 4).
struct TabLabelTests {
    let symbol: (String) -> Bool = { !$0.isEmpty }

    @Test func agentTabsAlwaysCarryTheAgent() {
        let team = [TeamAgent(id: "builder", name: "Builder", status: .running),
                    TeamAgent(id: "sly", name: "", avatar: "person.fill", status: .running)]
        let owner = TabLabel.of(TabInfo(id: "@1", name: "shell", active: true), team: team, isSymbol: symbol)
        #expect(owner == TabLabel(title: "shell", chip: nil))
        let agentTabs = [
            TabInfo(id: "builder:@1", name: "shell", active: false, agentID: "builder"),
            TabInfo(id: "sly:@1", name: "shell", active: false, agentID: "sly"),
            TabInfo(id: "ghost:@1", name: "shell", active: false, agentID: "ghost"),
        ]
        let labels = agentTabs.map { TabLabel.of($0, team: team, isSymbol: symbol) }
        #expect(labels.map(\.title) == ["Builder \u{00B7} shell", "sly \u{00B7} shell", "ghost \u{00B7} shell"])
        for l in labels {
            #expect(l != owner && l.title != owner.title)
            #expect(l.chip != nil && l.chip != AvatarLook.owner && l.chip?.colour != AvatarLook.owner.colour)
            #expect(l.chip?.symbol?.hasPrefix("person") != true)
        }
    }
}

/// AgentInfo's status, routines and routines_off reach the row and the
/// column (R-0256, R-0258).
struct TeamAgentStatusMappingTests {
    let now = Date(timeIntervalSince1970: 100_000)

    func info(_ state: Portenv_Types_V1_AgentState = .running, _ edit: (inout Portenv_Daemon_V1_AgentInfo) -> Void = { _ in }) -> TeamAgent {
        var a = Portenv_Daemon_V1_AgentInfo.with { $0.id = "builder"; $0.name = "Builder"; $0.state = state; $0.approved = true; $0.lastLine = "Board line" }
        edit(&a)
        return TeamAgent(a)
    }

    func status(_ text: String, done: Int32 = 0, total: Int32 = 0, blocked: String = "", minutesAgo: Double) -> Portenv_Types_V1_AgentStatus {
        .with {
            $0.text = text; $0.progressDone = done; $0.progressTotal = total; $0.blocked = blocked
            $0.setAt = .init(date: now.addingTimeInterval(-minutesAgo * 60))
        }
    }

    @Test func statusMapsOneToOne() {
        let a = info { $0.status = status("Migrating tables", done: 3, total: 5, minutesAgo: 4) }
        #expect(a.workStatus == AgentWorkStatus(text: "Migrating tables", progress: (3, 5), setAt: now.addingTimeInterval(-240)))
        #expect(TeamText.rowLine(a, now: now) == ("Migrating tables (3/5) \u{00B7} 4 min ago", false))
        #expect(TeamText.spoken(a, now: now) == "Builder, running. Migrating tables (3/5) \u{00B7} 4 min ago")
        // No progress given is both 0: none shown.
        #expect(info { $0.status = status("Reading", minutesAgo: 1) }.workStatus?.progress == nil)
        let b = info { $0.status = status("Deploying", blocked: "needs the staging key", minutesAgo: 2) }
        #expect(TeamText.rowLine(b, now: now).text == "blocked: needs the staging key \u{00B7} 2 min ago")
        #expect(TeamText.rowLine(info { $0.status = status("Old", minutesAgo: 35) }, now: now) == ("no status for 35 min", true))
    }

    @Test func noStatusKeepsTheBoardLine() {
        let a = info()
        #expect(a.workStatus == nil && a.routines.isEmpty && a.routinesOff.isEmpty)
        #expect(TeamText.rowLine(a, now: now) == ("Board line", false))
        // A stopped agent's old status isn't its row's line; nor does it
        // hide why an agent can't start.
        #expect(TeamText.rowLine(info(.stopped) { $0.status = status("Was testing", minutesAgo: 1) }, now: now).text == "Board line")
        let invalid = info(.invalid) { $0.problem = "Unknown key"; $0.status = status("x", minutesAgo: 1) }
        #expect(TeamText.rowLine(invalid, now: now).text == "Unknown key")
        #expect(TeamText.startFailure(invalid) == nil)
        // A start that failed is a stopped agent's line, never a running
        // one's (R-0302).
        let failed = info(.stopped) { $0.problem = "Couldn\u{2019}t start: an old toolbox" }
        #expect(TeamText.rowLine(failed, now: now).text == "Couldn\u{2019}t start: an old toolbox")
        #expect(TeamText.spoken(failed, now: now) == "Builder, stopped. Couldn\u{2019}t start: an old toolbox")
        #expect(TeamText.startFailure(info(.starting) { $0.problem = "Couldn\u{2019}t start: x" }) == nil)
    }

    @Test func routinesAndWhyTheyreOff() {
        let r = info {
            $0.routines = [.with { $0.everyMinutes = 30; $0.note = "Status round" }, .with { $0.everyMinutes = 120; $0.note = "Tidy\nup"; $0.paused = true }]
        }
        #expect(r.routines == [AgentRoutine(every: 1800, note: "Status round"), AgentRoutine(every: 7200, note: "Tidy up", paused: true)])
        #expect(TeamText.routinesLine(r) == "Routines: every 30 min, every 2 hours")
        let paused = info { $0.routines = [.with { $0.everyMinutes = 60; $0.note = "a"; $0.paused = true }] }
        #expect(TeamText.routinesLine(paused) == "Routines: every 1 hour (paused)")
        let off = info {
            $0.routines = [.with { $0.everyMinutes = 30; $0.note = "a" }]
            $0.routinesOff = "Routines are off: Portenv doesn\u{2019}t know when my-agent is idle.\n\u{1b}[2J"
        }
        #expect(off.routinesOff == "Routines are off: Portenv doesn\u{2019}t know when my-agent is idle. [2J")
        #expect(TeamText.routinesLine(off) == off.routinesOff)
        #expect(TeamText.routinesLine(info()) == nil)
    }
}
