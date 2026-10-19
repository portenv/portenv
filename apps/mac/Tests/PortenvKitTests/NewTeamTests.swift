// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation
import Testing

@testable import PortenvKit

/// portenvd's PlanTeam, played in memory: each file's bytes follow from
/// the choices, so a change changes the SHA-256 as the real one does.
final class FakeTeamPlanner: TeamPlanning, @unchecked Sendable {
    private let lock = NSLock()
    private var _allowed = 4
    private var _raisedTo: [Int] = []
    private var _plans = 0
    var raisedTo: [Int] { lock.withLock { _raisedTo } }
    var plans: Int { lock.withLock { _plans } }
    /// A plan call waits this long (to see answers arrive out of order).
    var delay: (@Sendable (TeamChoice) -> Duration)?

    static let teams: [(id: String, name: String, agents: [(id: String, name: String, on: Bool)])] = [
        ("starter", "Starter", [("lead", "Lead", true), ("builder", "Builder", true), ("reviewer", "Reviewer", true)]),
        ("orchestrate", "Orchestrate", [("coordinator", "Coordinator", true), ("reviewer", "Reviewer", true), ("builder-a", "Builder A", true),
                                         ("builder-b", "Builder B", true), ("builder-c", "Builder C", true), ("devops", "DevOps", true),
                                         ("design-system", "Design keeper", false)]),
    ]

    func teamTemplates() async throws -> [TeamTemplateInfo] {
        var out: [TeamTemplateInfo] = []
        for t in Self.teams {
            let plan = try await planTeam(TeamChoice(template: t.id, agents: []))
            out.append(TeamTemplateInfo(id: t.id, name: t.name, description: t.agents.filter(\.on).map(\.name).joined(separator: ", "), agents: plan.agents))
        }
        return out
    }

    func planTeam(_ choice: TeamChoice) async throws -> TeamPlan {
        if let delay { try? await Task.sleep(for: delay(choice)) }
        let allowed = lock.withLock { _plans += 1; return _allowed }
        guard let t = Self.teams.first(where: { $0.id == choice.template }) else { throw DaemonError("no template") }
        var agents: [PlannedAgentFile] = []
        for a in t.agents {
            let c = choice.agents.first { $0.id == a.id }
            let include = c?.include ?? a.on
            let name = c.map { $0.name.isEmpty ? a.name : $0.name } ?? a.name
            if name.contains(":") { throw DaemonError("\(a.id)'s name: a name has letters, numbers and spaces") }
            let model = c?.model ?? ""
            let content = "---\nid: \(a.id)\nname: \(name)\nrunner: claude\(model.isEmpty ? "" : " --model " + model)\n---\nYou are \(name).\n"
            let sha = SHA256.hash(data: Data(content.utf8)).map { String(format: "%02x", $0) }.joined()
            var changes: [String] = []
            if !model.isEmpty { changes.append("runner claude (default) \u{2192} claude (\(model))") }
            agents.append(PlannedAgentFile(id: a.id, name: name, role: "Role", avatar: "blue", templateName: a.name, included: include,
                                           templateIncluded: a.on, path: ".portenv/agents/" + (include ? "" : "off/") + a.id + ".md",
                                           content: content, sha256: sha, runner: "claude", runnerName: "Claude Code",
                                           runnerLine: "claude" + (model.isEmpty ? "" : " --model " + model), model: model, templateModel: "",
                                           models: ["", "opus", "sonnet", "fable"], templateTeamStatus: false, teamStatus: c?.teamStatus ?? false,
                                           changes: changes, grants: ["May ask you."], instructions: "You are \(name).\n", problems: []))
        }
        let n = agents.filter(\.included).count
        let problems = n > allowed ? ["This team has \(n) agents, and you allow \(allowed) in a workspace. Leave \(n - allowed) out, or raise Agents allowed to \(n)."] : []
        return TeamPlan(agents: agents, included: n, allowed: allowed, problems: problems)
    }

    func agentsAllowed() async throws -> Int { lock.withLock { _allowed } }

    func setAgentsAllowed(_ n: Int) async throws -> Int {
        lock.withLock { _allowed = n; _raisedTo.append(n); return n }
    }
}

@MainActor
struct NewTeamTests {
    func loaded() async -> (NewTeamFlow, FakeTeamPlanner) {
        let planner = FakeTeamPlanner()
        let team = NewTeamFlow(planner: planner)
        await team.load()
        return (team, planner)
    }

    /// None (just you) until the owner picks a team; the built-ins listed
    /// as portenvd gives them.
    @Test func noneUntilATeamIsPicked() async {
        let (team, _) = await loaded()
        #expect(team.template == nil && team.choice == nil && team.canContinue)
        #expect(team.templates.map(\.id) == ["starter", "orchestrate"])
        #expect(team.templates[0].description == "Lead, Builder, Reviewer")
        #expect(NewTeamText.noneTitle == "None" && NewTeamText.noneLine == "Just you. Add agents later.")
    }

    @Test func pickingATeamPlansIt() async {
        let (team, _) = await loaded()
        await team.choose("starter")
        #expect(team.template == "starter")
        #expect(team.included.map(\.id) == ["lead", "builder", "reviewer"])
        #expect(team.canContinue && team.whyNotContinue == nil)
    }

    /// More agents than the owner allows: Continue says why, and offers
    /// the Raise, confirmed first, through portenvd (R-0347).
    @Test func overTheLimitOffersTheRaise() async throws {
        let (team, planner) = await loaded()
        await team.choose("orchestrate")
        #expect(!team.canContinue)
        #expect(team.whyNotContinue == "This team has 6 agents, and you allow 4 in a workspace. Leave 2 out, or raise Agents allowed to 6.")
        let raise = try #require(team.raise)
        #expect(raise.raiseTitle == "Raise to 6\u{2026}" && raise.confirmButton == "Allow 6")
        await team.raiseLimit()
        #expect(planner.raisedTo == [6] && team.allowed == 6 && team.canContinue && team.raise == nil)
    }

    @Test func leavingAgentsOutBringsItUnder() async {
        let (team, _) = await loaded()
        await team.choose("orchestrate")
        await team.setIncluded("builder-b", false)
        await team.setIncluded("builder-c", false)
        #expect(team.included.count == 4 && team.canContinue)
        #expect(team.agents.first { $0.id == "builder-c" }?.path == ".portenv/agents/off/builder-c.md")
    }

    /// Review and approve (ADR 0025 §4): Approve only after the file was
    /// opened to its end; Approve All only after every one was; Create
    /// only after every one is approved.
    @Test func approvalsNeedEachFileOpened() async throws {
        let (team, _) = await loaded()
        await team.choose("starter")
        #expect(!team.canCreate && team.whyNotCreate == "Approve Lead, Builder and Reviewer to create the workspace.")
        #expect(!team.canApprove("lead") && team.reviewState("lead") == .notOpened)
        #expect(!team.approveAllEnabled && team.approveAllLine == "Open every agent first (3 left).")
        team.opened("lead")
        #expect(team.reviewState("lead") == .opened && team.canApprove("lead"))
        team.approve("lead")
        #expect(team.reviewState("lead") == .approved)
        team.opened("builder")
        #expect(team.approveAllLine == "Open every agent first (1 left).")
        team.opened("reviewer")
        #expect(team.approveAllEnabled && team.approveAllLine == nil)
        team.approveAll()
        #expect(team.canCreate && team.whyNotCreate == nil)
        let choice = try #require(team.choice)
        #expect(choice.template == "starter")
        for a in team.included {
            #expect(choice.agents.first { $0.id == a.id }?.approvedSHA256 == a.sha256)
        }
    }

    /// A file that changes after it was approved (or opened) goes back to
    /// changed, not opened; the others keep theirs.
    @Test func aChangeAfterApprovalNeedsItAgain() async {
        let (team, _) = await loaded()
        await team.choose("starter")
        for id in ["lead", "builder", "reviewer"] { team.opened(id) }
        team.approveAll()
        await team.setModel("reviewer", "opus")
        #expect(team.reviewState("reviewer") == .changed && !team.canApprove("reviewer"))
        #expect(team.reviewState("lead") == .approved && !team.canCreate)
        #expect(team.whyNotCreate == "Approve Reviewer to create the workspace.")
        #expect(team.choice?.agents.first { $0.id == "reviewer" }?.approvedSHA256 == "")
        team.opened("reviewer")
        team.approve("reviewer")
        #expect(team.canCreate)
    }

    /// Leave Out in the review takes the agent out of the team.
    @Test func leaveOutInTheReview() async {
        let (team, _) = await loaded()
        await team.choose("starter")
        await team.leaveOut("builder")
        #expect(team.included.map(\.id) == ["lead", "reviewer"])
    }

    @Test func aRefusedNameIsSaid() async {
        let (team, _) = await loaded()
        await team.choose("starter")
        await team.rename("lead", "a: b")
        #expect(!team.canContinue && team.whyNotContinue?.contains("name") == true)
        await team.rename("lead", "Chief")
        #expect(team.canContinue && team.included.first?.name == "Chief")
    }

    /// Typing fast: an older plan arriving last never replaces a newer one.
    @Test func anOlderPlanNeverWins() async {
        let planner = FakeTeamPlanner()
        planner.delay = { c in c.agents.first { $0.id == "lead" }?.name == "Slow" ? .milliseconds(150) : .zero }
        let team = NewTeamFlow(planner: planner)
        await team.load()
        await team.choose("starter")
        async let slow: Void = team.rename("lead", "Slow")
        try? await Task.sleep(for: .milliseconds(20))
        await team.rename("lead", "Fast")
        await slow
        #expect(team.included.first?.name == "Fast")
    }

    @Test func choosingNoneDropsTheTeam() async {
        let (team, _) = await loaded()
        await team.choose("starter")
        await team.choose(nil)
        #expect(team.choice == nil && team.agents.isEmpty && team.canContinue)
    }
}

/// New workspace with the Team step (ADR 0025 §1): Name, Team, Review and
/// approve, then Creating.
@MainActor
struct NewWorkspaceTeamStepTests {
    @Test func theStepsWithATeamPlanner() {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner())
        #expect(flow.steps == [.firstBox, .team, .review, .gettingReady])
        let first = FirstRunFlow(service: FakeFirstRunService(), teamPlanner: FakeTeamPlanner())
        #expect(first.steps == [.welcome, .recoveryKey, .storage, .firstBox, .team, .review, .gettingReady])
        let without = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox)
        #expect(without.steps == [.firstBox, .gettingReady])
    }

    /// With None, the Team step creates the workspace, with no team.
    @Test func noneCreatesFromTheTeamStep() async {
        let service = FakeFirstRunService()
        let flow = FirstRunFlow(service: service, at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner())
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .team && service.created.isEmpty)
        await flow.team.load()
        #expect(flow.primaryButton == "Create Workspace")
        await flow.next()
        #expect(flow.step == .gettingReady && service.created.count == 1 && service.created[0].team == nil)
    }

    /// With a team: Review, then Create Workspace only once every file is
    /// approved; CreateBox gets the choice with each approved SHA-256.
    @Test func aTeamIsCreatedOnlyApproved() async throws {
        let service = FakeFirstRunService()
        let flow = FirstRunFlow(service: service, at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner())
        flow.boxName = "acme-api"
        await flow.next()
        await flow.team.load()
        await flow.team.choose("starter")
        #expect(flow.primaryButton == "Continue")
        await flow.next()
        #expect(flow.step == .review && flow.primaryButton == "Create Workspace")
        await flow.next()
        #expect(flow.step == .review && service.created.isEmpty && flow.whyNot == "Approve Lead, Builder and Reviewer to create the workspace.")
        for id in ["lead", "builder", "reviewer"] { flow.team.opened(id) }
        flow.team.approveAll()
        await flow.next()
        #expect(flow.step == .gettingReady)
        let team = try #require(service.created.first?.team)
        #expect(team.template == "starter" && team.agents.allSatisfy { !$0.approvedSHA256.isEmpty })
        #expect(FirstRunText.teamStartLine == "Agents work in their own copies. Check their branches before you run them.")
    }

    /// Over the limit, the Team step's Continue says why.
    @Test func overTheLimitHoldsTheTeamStep() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner())
        flow.boxName = "acme-api"
        await flow.next()
        await flow.team.load()
        await flow.team.choose("orchestrate")
        await flow.next()
        #expect(flow.step == .team && flow.whyNot?.hasPrefix("This team has 6 agents") == true)
    }

    @Test func backFromReviewKeepsTheTeam() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner())
        flow.boxName = "acme-api"
        await flow.next()
        await flow.team.load()
        await flow.team.choose("starter")
        await flow.next()
        flow.back()
        #expect(flow.step == .team && flow.team.template == "starter")
    }
}
