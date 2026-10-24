// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// New workspace's steps at the window's minimum size and full screen (the
/// owner's dev build at 2c40986): the Team step was 880 pt wide in a 720 pt
/// window, cut off on both sides; its agent list was a 300 pt box however
/// tall the window, hiding later agents.
@MainActor
@Suite(.serialized)
struct NewWorkspaceStepsLayoutTests {
    private struct Service: FirstRunService {
        func makeRecoveryKey() async throws -> String { "" }
        func createBox(_: NewBox) async throws {}
    }

    /// A team of `count` agents, as portenvd plans it.
    private struct Planner: TeamPlanning {
        let count: Int

        func teamTemplates() async throws -> [TeamTemplateInfo] {
            [TeamTemplateInfo(id: "big", name: "App product", description: "Engineering Lead, Code Writer, Code Reviewer, Product Designer, "
                                  + "Design System Keeper, Trust & Safety and Support, App Store Release Manager, DevOps", agents: agents()),
             TeamTemplateInfo(id: "starter", name: "Starter", description: "Lead, Builder, Reviewer", agents: []),
             TeamTemplateInfo(id: "orchestrate", name: "Orchestrate", description: "Coordinator, Reviewer, Builder A, Builder B, Builder C, DevOps", agents: [])]
        }

        func planTeam(_: TeamChoice) async throws -> TeamPlan {
            TeamPlan(agents: agents(), included: count, allowed: 10, problems: [])
        }

        func agentsAllowed() async throws -> Int { 10 }
        func setAgentsAllowed(_ n: Int) async throws -> Int { n }

        private func agents() -> [PlannedAgentFile] {
            (0..<count).map { i in
                let content = "---\nid: agent-\(i)\n---\nYou are agent \(i).\n"
                return PlannedAgentFile(id: "agent-\(i)", name: "Agent \(i)", role: "Does part \(i) of the work", avatar: "indigo",
                                        templateName: "Agent \(i)", included: true, templateIncluded: true, path: ".portenv/agents/agent-\(i).md",
                                        content: content, sha256: SHA256Text.of(content), runner: "claude", runnerName: "Claude Code",
                                        runnerLine: "claude", model: "", templateModel: "", models: ["", "opus", "sonnet"],
                                        templateTeamStatus: false, teamStatus: false, changes: [], grants: ["May ask you."],
                                        instructions: "You are agent \(i).\n", problems: [])
            }
        }
    }

    static let minimum = CGSize(width: 720, height: 520)
    static let fullScreen = CGSize(width: 1512, height: 945)

    /// A New workspace flow at each step with a team chosen.
    private func makeFlow(agents: Int = 8) -> FirstRunFlow {
        let flow = FirstRunFlow(service: Service(), at: .firstBox, fromBoxList: true, teamPlanner: Planner(count: agents))
        flow.boxName = "acme-api"
        return flow
    }

    private func walk(_ flow: FirstRunFlow, to step: FirstRunStep) async {
        while true {
            if flow.step == .project { flow.projectKind = .empty }
            if flow.step == .team, flow.team.template == nil {
                await flow.team.load()
                await flow.team.choose("big")
            }
            if flow.step == step { return }
            let before = flow.step
            await flow.next()
            #expect(flow.step != before, "stuck at \(before): \(flow.reason ?? flow.error ?? "")")
            if flow.step == before { return }
        }
    }

    private func view(_ flow: FirstRunFlow) -> some View {
        FirstRunView(flow: flow, opened: { _ in }, cancel: {}, followsDocker: false).environment(\.isRendering, true)
    }

    /// Every step lays out within the minimum window: never wider, never
    /// taller (a taller step would be cut off, as a wider one was).
    @Test func everyStepFitsTheMinimumWindow() async {
        _ = NSApplication.shared
        for step in [FirstRunStep.firstBox, .project, .team, .review] {
            let flow = makeFlow()
            await walk(flow, to: step)
            let host = NSHostingController(rootView: view(flow))
            let fit = host.sizeThatFits(in: Self.minimum)
            #expect(fit.width <= Self.minimum.width + 0.5, "\(step) wants \(fit.width) pt of width in \(Self.minimum.width)")
            #expect(fit.height <= Self.minimum.height + 0.5, "\(step) wants \(fit.height) pt of height in \(Self.minimum.height)")
        }
    }

    /// The Team step's own content, at the minimum window's width less its
    /// margins: the cards and the agent rows stay inside it.
    @Test func theTeamStepsContentFitsTheMinimumWidth() async {
        _ = NSApplication.shared
        let flow = makeFlow()
        await walk(flow, to: .team)
        let width = Self.minimum.width - 2 * 64
        let host = NSHostingController(rootView: TeamStepView(team: flow.team).environment(\.isRendering, true))
        let fit = host.sizeThatFits(in: CGSize(width: width, height: .infinity))
        #expect(fit.width <= width + 0.5, "the Team step wants \(fit.width) pt in \(width)")
    }

    /// The agent list is as tall as its rows: never a fixed box that hides
    /// later agents however much room the window has.
    @Test func theAgentListGrowsWithItsAgents() async {
        _ = NSApplication.shared
        func height(_ agents: Int) async -> CGFloat {
            let flow = makeFlow(agents: agents)
            await walk(flow, to: .team)
            let host = NSHostingController(rootView: TeamStepView(team: flow.team).environment(\.isRendering, true))
            return host.sizeThatFits(in: CGSize(width: Self.fullScreen.width - 2 * 64, height: .infinity)).height
        }
        let three = await height(3)
        let eight = await height(8)
        // Each row is at least 36 pt (a field, the id under it).
        #expect(eight - three >= 5 * 36, "8 agents are only \(eight - three) pt taller than 3")
    }

    /// Full screen, the whole team shows without scrolling: the step's
    /// natural height fits the window.
    @Test func fullScreenShowsEveryAgent() async {
        _ = NSApplication.shared
        let flow = makeFlow(agents: 8)
        await walk(flow, to: .team)
        let width = Self.fullScreen.width - 2 * 64
        let host = NSHostingController(rootView: TeamStepView(team: flow.team).environment(\.isRendering, true))
        let natural = host.sizeThatFits(in: CGSize(width: width, height: .infinity)).height
        // The window less its margins, the heading and the footer.
        #expect(natural <= Self.fullScreen.height - 96 - 200, "8 agents need \(natural) pt")
    }
}
