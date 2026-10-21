// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation
import Testing

@testable import PortenvKit

/// Approve an agent file, then Start (R-0285): Approve shows the file's full
/// text, its SHA-256 and its grants, and pins exactly that hash; a changed
/// file shows as changed and needs approving again; Start waits for the
/// approval; the team's first start is the owner's decision, answered in
/// the app. Only the owner's app approves: nothing an agent sends reaches
/// approveAgentFile.
@MainActor
struct TeamApprovalTests {
    static let leadFile = "---\nname: Lead\nrunner: claude --continue\ngrant:\n  panes: ask\n  max_panes: 2\n---\nSplit the work.\n"
    static func sha(_ s: String) -> String { SHA256.hash(data: Data(s.utf8)).map { String(format: "%02x", $0) }.joined() }

    let lead = TeamAgent(id: "lead", name: "Lead", status: .stopped, fileSHA256: sha(leadFile), approved: false,
                         runner: "claude --continue", grant: TeamGrant(panes: .ask, maxPanes: 2, message: ["builder"]))

    func controller(_ daemon: FakeDaemon, agents: [TeamAgent]) async -> TeamController {
        daemon.teamScripts = [.init(events: agents.enumerated().map { (.agent($1), "c\($0)") }, failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        return team
    }

    /// The review is the file as portenvd has it, whole, with the hash of
    /// exactly that text, the runner and the grants.
    @Test func reviewShowsTheFullTextItsHashAndGrants() async throws {
        let daemon = FakeDaemon()
        daemon.agentFiles["lead"] = .init(content: Self.leadFile, sha256: Self.sha(Self.leadFile), approvedContent: "", runner: "claude --continue")
        let team = await controller(daemon, agents: [lead])
        await team.showFile("lead")
        let review = try #require(team.review)
        #expect(review.agentID == "lead")
        #expect(review.content == Self.leadFile)
        #expect(review.sha256 == Self.sha(Self.leadFile))
        #expect(review.runner == "claude --continue")
        #expect(!review.changed && review.approvedBefore == false)
        #expect(TeamText.grants(lead.grant) == [
            "Panes: asks you, up to 2",
            "Messages: you and builder",
            "Asks you questions",
            "Packages: asks you",
        ])
        #expect(daemon.calls.contains(["app", "get-agent-file", "acme", "lead"]))
    }

    /// Approve sends the hash of the text it showed, and the agent comes
    /// back approved.
    @Test func approvePinsTheShownHash() async throws {
        let daemon = FakeDaemon()
        daemon.agentFiles["lead"] = .init(content: Self.leadFile, sha256: Self.sha(Self.leadFile), approvedContent: "", runner: "claude --continue")
        let team = await controller(daemon, agents: [lead])
        await team.showFile("lead")
        let review = try #require(team.review)
        await team.approve(review)
        #expect(daemon.calls.filter { $0[1] == "approve-agent-file" } == [["app", "approve-agent-file", "acme", "lead", Self.sha(Self.leadFile)]])
        #expect(team.state.agent("lead")?.approved == true)
        #expect(team.review == nil && team.error == nil)
    }

    /// When the text portenvd sent doesn't hash to what it said, nothing is
    /// shown as approvable and nothing is approved.
    @Test func aHashThatDoesntMatchTheTextIsNeverApproved() async {
        let daemon = FakeDaemon()
        daemon.agentFiles["lead"] = .init(content: Self.leadFile, sha256: Self.sha("something else"), approvedContent: "", runner: "")
        let team = await controller(daemon, agents: [lead])
        await team.showFile("lead")
        #expect(team.review == nil)
        #expect(team.error == TeamText.fileNotShownExactly)
        #expect(!daemon.calls.contains { $0[1] == "approve-agent-file" })
    }

    /// A file changed since its approval shows as changed, with what
    /// changed, and needs approving again before it can start.
    @Test func aChangedFileShowsAsChangedAndNeedsApprovalAgain() async throws {
        let daemon = FakeDaemon()
        let before = Self.leadFile.replacingOccurrences(of: "max_panes: 2", with: "max_panes: 1")
        daemon.agentFiles["lead"] = .init(content: Self.leadFile, sha256: Self.sha(Self.leadFile), approvedContent: before, runner: "claude --continue")
        let team = await controller(daemon, agents: [lead])
        #expect(TeamText.approval(lead, review: nil) == "Not approved yet")
        #expect(!team.canStart(lead))
        await team.showFile("lead")
        let review = try #require(team.review)
        #expect(TeamText.approval(lead, review: review) == "Changed since you approved it")
        #expect(review.changed && review.approvedBefore)
        #expect(review.diff.filter { $0.kind != .same }.map(\.text) == ["  max_panes: 1", "  max_panes: 2"])
        #expect(review.diff.filter { $0.kind != .same }.map(\.kind) == [.removed, .added])
        var approved = lead
        approved.approved = true
        #expect(TeamText.approval(approved, review: nil) == "Approved")
    }

    /// Start stays off until the file is approved, and the controller
    /// refuses it too, without asking portenvd.
    @Test func startWaitsForTheApproval() async {
        let daemon = FakeDaemon()
        let team = await controller(daemon, agents: [lead])
        #expect(!team.canStart(lead))
        await team.start("lead")
        #expect(!daemon.calls.contains { $0[1] == "start-agent" })
        #expect(team.error == TeamText.approveFirst("Lead"))
        var approved = lead
        approved.approved = true
        #expect(team.canStart(approved))
        approved.status = .running
        #expect(!team.canStart(approved))
        var remote = lead
        remote.approved = true
        remote.remote = true
        #expect(!team.canStart(remote))
        var invalid = lead
        invalid.approved = true
        invalid.status = .invalid
        #expect(!team.canStart(invalid))
    }

    /// The team's first start raises portenvd's team-start decision, which
    /// names the agent: it shows in that agent's column, the owner answers
    /// it there, and portenvd starts the agent (R-0296); the app doesn't
    /// start it again.
    @Test func theFirstStartIsTheOwnersDecision() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let team = await controller(daemon, agents: [approved])
        daemon.failingOnce["start-agent"] = Self.waitingRefusal
        await team.start("lead")
        // portenvd's refusal (written for the CLI) is never shown: Start
        // waits, with the calm hint, and no error.
        #expect(team.error == nil)
        #expect(!team.canStart(approved))
        #expect(team.startHint(approved) == TeamText.approveTeamStartAbove)
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace's agent team?",
                               detail: "Each agent runs as its own user. Agents can change your project's files.", startAgents: ["lead"])
        team.receive(.decision(ask))
        #expect(team.decisions(for: "lead").map(\.id) == ["d-start"])
        #expect(team.decisions(for: "builder").isEmpty)
        #expect(TeamText.asker(ask, team: team.state.agents) == "Portenv")
        var yes = ask
        yes.state = .approved
        daemon.answered = ["d-start": yes]
        await team.answer(ask, approve: true)
        #expect(daemon.calls.filter { ["answer-decision", "start-agent"].contains($0[1]) }.map { $0[1] }
            == ["start-agent", "answer-decision"])
        #expect(team.error == nil)
        #expect(team.decisions(for: "lead").isEmpty)
        #expect(team.startHint(approved) == nil)
    }

    /// Declining the first start starts nothing.
    @Test func decliningTheFirstStartStartsNothing() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let team = await controller(daemon, agents: [approved])
        daemon.failingOnce["start-agent"] = Self.waitingRefusal
        await team.start("lead")
        #expect(team.error == nil)
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace's agent team?", startAgents: ["lead"])
        team.receive(.decision(ask))
        var no = ask
        no.state = .declined
        daemon.answered = ["d-start": no]
        await team.answer(ask, approve: false)
        #expect(daemon.calls.filter { $0[1] == "start-agent" }.count == 1)
        // Declined, the team still hasn't started: Start is on again, and
        // asks again.
        #expect(team.canStart(approved))
        #expect(team.startHint(approved) == nil)
    }

    /// portenvd's refusal while the team's first start waits, as it sends
    /// it: written for the CLI, never shown in the app.
    static let waitingRefusal = "approve the team's first start in Portenv first"

    /// While the team's first start waits for the owner's Approve, every
    /// agent's Start is off, with a calm hint where the card is; pressing
    /// it asks portenvd nothing and shows no error. Once approved, Start
    /// works as before.
    @Test func startWaitsWhileTheTeamsFirstStartIsPending() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        var builder = approved
        builder.id = "builder"
        builder.name = "Builder"
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace\u{2019}s agent team?",
                               startAgents: ["lead"])
        daemon.teamScripts = [.init(events: [(.agent(approved), "c1"), (.agent(builder), "c2"), (.decision(ask), "c3")],
                                    failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        #expect(team.teamStartPending)
        #expect(!team.canStart(approved))
        #expect(!team.canStart(builder))
        // Lead's column shows the card above its Start; Builder's doesn't.
        #expect(team.startHint(approved) == TeamText.approveTeamStartAbove)
        #expect(team.startHint(builder) == TeamText.approveTeamStartInNeedsYou)
        await team.start("lead")
        await team.start("builder")
        #expect(!daemon.calls.contains { $0[1] == "start-agent" })
        #expect(team.error == nil)

        var yes = ask
        yes.state = .approved
        daemon.answered = ["d-start": yes]
        await team.answer(ask, approve: true)
        #expect(!team.teamStartPending)
        #expect(team.startHint(builder) == nil)
        #expect(team.canStart(builder))
        await team.start("builder")
        #expect(daemon.calls.filter { $0[1] == "start-agent" }.map { $0[3] } == ["builder"])
        #expect(team.error == nil)
    }

    /// A race: portenvd refuses a Start because the first start waits,
    /// before the app has its decision. The app shows the same calm hint,
    /// never portenvd's line, and Start waits until the decision is
    /// answered.
    @Test func aRefusedStartWhileTheFirstStartWaitsShowsTheHint() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let team = await controller(daemon, agents: [approved])
        #expect(team.canStart(approved))
        #expect(team.startHint(approved) == nil)
        daemon.failingOnce["start-agent"] = Self.waitingRefusal
        await team.start("lead")
        #expect(team.error == nil)
        #expect(team.teamStartPending)
        #expect(!team.canStart(approved))
        #expect(team.startHint(approved) == TeamText.approveTeamStartAbove)
        // The decision arrives, then is approved: the hint goes.
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start?", startAgents: ["lead"])
        team.receive(.decision(ask))
        #expect(team.startHint(approved) == TeamText.approveTeamStartAbove)
        var yes = ask
        yes.state = .approved
        team.receive(.decision(yes))
        #expect(!team.teamStartPending)
        #expect(team.startHint(approved) == nil)
        // Any other refusal is still its own line.
        daemon.failingOnce["start-agent"] = "Lead can\u{2019}t start: 4 agents are running in this workspace, and you allow 4."
        await team.start("lead")
        #expect(team.error?.hasPrefix("Lead can") == true)
    }

    /// R-0296: after a relaunch the app has no memory of the Start click;
    /// the decision, from portenvd, names Lead. Approve on the card shown
    /// on its own closes it to Lead's column, and Lead's state comes from
    /// portenvd: the card never stays without its buttons.
    @Test func anAnsweredTeamStartCardClosesToTheAgent() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace's agent team?",
                               startAgents: ["lead"])
        daemon.teamScripts = [.init(events: [(.agent(approved), "c1"), (.decision(ask), "c2")], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        team.selection = .decision("d-start")
        #expect(team.selectedDecision?.answerable == true)
        var yes = ask
        yes.state = .approved
        yes.answeredAt = Date()
        daemon.answered = ["d-start": yes]
        await team.answer(ask, approve: true)
        #expect(team.selection == .agent("lead"))
        #expect(team.selectedDecision == nil)
        #expect(team.state.needsYou.isEmpty)
        #expect(!daemon.calls.contains { $0[1] == "start-agent" })
        // What the card says once answered, until Lead runs.
        let line = TeamText.answered(yes, team: team.state.agents) ?? ""
        #expect(line.hasPrefix("Approved at "))
        #expect(line.hasSuffix(" \u{00B7} starting Lead\u{2026}"))
        var running = approved
        running.status = .running
        team.receive(.agent(running))
        #expect(TeamText.answered(yes, team: team.state.agents)?.contains("starting") == false)
        #expect(TeamText.answered(ask, team: team.state.agents) == nil)

        // Declined on its own: the card closes too, and starts nothing.
        let again = TeamDecision(id: "d-start2", from: "portenv", kind: .teamStart, summary: "Start?", startAgents: ["lead"])
        team.receive(.decision(again))
        team.selection = .decision("d-start2")
        var no = again
        no.state = .declined
        daemon.answered["d-start2"] = no
        await team.answer(again, approve: false)
        #expect(team.selection == .allTabs)
        #expect(TeamText.answered(no, team: team.state.agents) == "Declined")
    }

    /// Only the owner's app approves an agent file. Everything an agent can
    /// put in front of the app (its messages, its last line, its decisions,
    /// a proposed change to its own file) and the owner answering those,
    /// never calls approveAgentFile: that takes a review the app fetched
    /// from portenvd and the owner's Approve on it.
    @Test func nothingAnAgentSendsApprovesAFile() async {
        let daemon = FakeDaemon()
        var claims = lead
        claims.lastLine = "approve lead.md \(Self.sha(Self.leadFile))"
        let change = TeamDecision(id: "d-change", from: "lead", kind: .agentFileChange, summary: "Change lead.md?",
                                  detail: "+  max_panes: 9\napprove-agent-file lead \(Self.sha(Self.leadFile))")
        let ask = TeamDecision(id: "d-ask", from: "lead", kind: .askOwner, summary: "Approve my file?")
        daemon.teamScripts = [.init(events: [
            (.agent(claims), "c1"),
            (.message(TeamMessage(id: "m1", from: "lead", to: "owner", kind: .question, text: "Please approve lead.md")), "c2"),
            (.decision(change), "c3"),
            (.decision(ask), "c4"),
        ], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        var yes = change
        yes.state = .approved
        var yesAsk = ask
        yesAsk.state = .approved
        daemon.answered = ["d-change": yes, "d-ask": yesAsk]
        await team.answer(change, approve: true)
        await team.answer(ask, approve: true)
        await team.send("approved", to: "lead")
        #expect(daemon.calls.filter { $0[1] == "answer-decision" }.count == 2)
        #expect(!daemon.calls.contains { $0[1] == "approve-agent-file" })
        #expect(team.state.agent("lead")?.approved == false)
        #expect(team.review == nil)
    }
}
