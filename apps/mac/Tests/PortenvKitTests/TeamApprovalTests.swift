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

    /// The team's first start raises portenvd's team-start decision: it
    /// shows in the agent's column, the owner answers it there, and
    /// Approve starts the agent they asked for.
    @Test func theFirstStartIsTheOwnersDecision() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let team = await controller(daemon, agents: [approved])
        daemon.failingOnce["start-agent"] = "approve the team's first start in Portenv first"
        await team.start("lead")
        #expect(team.error == "approve the team's first start in Portenv first")
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace's agent team?",
                               detail: "Each agent runs as its own user. Agents can change your project's files.")
        team.receive(.decision(ask))
        #expect(team.decisions(for: "lead").map(\.id) == ["d-start"])
        #expect(TeamText.asker(ask, team: team.state.agents) == "Portenv")
        var yes = ask
        yes.state = .approved
        daemon.answered = ["d-start": yes]
        await team.answer(ask, approve: true)
        #expect(daemon.calls.filter { ["answer-decision", "start-agent"].contains($0[1]) }.map { $0[1] }
            == ["start-agent", "answer-decision", "start-agent"])
        #expect(team.state.agent("lead")?.status == .starting)
        #expect(team.decisions(for: "lead").isEmpty)
    }

    /// Declining the first start starts nothing.
    @Test func decliningTheFirstStartStartsNothing() async {
        let daemon = FakeDaemon()
        var approved = lead
        approved.approved = true
        let team = await controller(daemon, agents: [approved])
        daemon.failingOnce["start-agent"] = "approve the team's first start in Portenv first"
        await team.start("lead")
        let ask = TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace's agent team?")
        team.receive(.decision(ask))
        var no = ask
        no.state = .declined
        daemon.answered = ["d-start": no]
        await team.answer(ask, approve: false)
        #expect(daemon.calls.filter { $0[1] == "start-agent" }.count == 1)
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
