// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The Team sidebar for `--render-screens`, beside the team-* mockups:
/// the board's team, with Needs you, All tabs and Builder selected, and
/// compact below 900 pt.
@MainActor
enum TeamRenders {
    static func sample(now: Date) -> (TeamState, [TabInfo]) {
        var s = TeamState()
        let grant = TeamGrant(panes: .ask, maxPanes: 4)
        for a in [
            TeamAgent(id: "lead", name: "Lead", status: .idle, lastLine: "Split the retry work in two", runner: "claude", grant: grant),
            TeamAgent(id: "builder", name: "Builder", status: .running, lastLine: "Running the payments tests", runner: "claude", grant: grant),
            TeamAgent(id: "reviewer", name: "Reviewer", status: .needsYou, lastLine: "Wants to merge #42", pendingDecisions: 1, runner: "claude"),
            TeamAgent(id: "keeper", name: "Design System Keeper", status: .idle, lastLine: "Icon set drafted", runner: "claude"),
            TeamAgent(id: "designer", name: "designer.md", status: .invalid, problem: "Unknown key \u{201C}grants\u{201D} on line 6"),
        ] { s.apply(.agent(a)) }
        for (i, (from, to, text, minutes)) in [
            ("lead", "builder", "Take the retry half; keep the payments API unchanged.", 34),
            ("builder", TeamText.owner, "Split done. Tests are running in my second pane.", 29),
            (TeamText.owner, "builder", "Ping me before touching the refunds module.", 27),
            ("builder", TeamText.owner, "Will do.", 26),
            ("builder", "reviewer", "The fake clock fix is on retries-clock.", 25),
            ("reviewer", "builder", "Looks right. One question about the jitter: should it come from the same clock?", 23),
        ].enumerated() {
            s.apply(.message(TeamMessage(id: "m\(i)", from: from, to: to, text: text, createdAt: now.addingTimeInterval(Double(-minutes * 60)))))
        }
        s.apply(.decision(TeamDecision(id: "d1", from: "reviewer", kind: .askOwner, summary: "merge #42?",
                                       detail: "Checks pass.", createdAt: now.addingTimeInterval(-6 * 60 - 10))))
        let tabs = [
            TabInfo(id: "@1", name: "shell", active: false),
            TabInfo(id: "@2", name: "notes", active: false),
            // Builder's main session is on screen (R-0253's mark).
            TabInfo(id: "builder:@1", name: "main session", active: true, agentID: "builder"),
            TabInfo(id: "builder:@2", name: "tests", active: false, command: "npm test --watch", agentID: "builder"),
            TabInfo(id: "builder:@3", name: "helper", active: false, command: "retries-refactor", agentID: "builder"),
            TabInfo(id: "reviewer:@1", name: "review #42", active: false, command: "npm test", agentID: "reviewer"),
        ]
        return (s, tabs)
    }

    static func render(to dir: URL, snapshot: (AnyView, String, NSSize) throws -> Void) throws {
        let now = Date()
        let (state, tabs) = sample(now: now)
        for appearance in ["light", "dark"] {
            func sidebar(_ selection: TeamSelection, compact: Bool = false) -> AnyView {
                AnyView(TeamSidebar(state: state, tabs: tabs, selection: .constant(selection), now: now, compact: compact)
                    .frame(maxHeight: .infinity, alignment: .top)
                    // Drawn as in the key window, as a tester sees it.
                    .environment(\.controlActiveState, .key))
            }
            try snapshot(sidebar(.allTabs), "team-sidebar-\(appearance)", NSSize(width: TeamSidebar.width, height: 640))
            try snapshot(sidebar(.agent("builder")), "team-sidebar-builder-\(appearance)", NSSize(width: TeamSidebar.width, height: 640))
            try snapshot(sidebar(.agent("builder"), compact: true), "team-sidebar-compact-\(appearance)", NSSize(width: TeamSidebar.compactWidth, height: 640))
            // Approve and Start (R-0285): a new file, a changed one, and the
            // team's first start.
            for (name, review) in approvals() {
                try snapshot(AnyView(AgentFileReviewCard(agent: approvalAgent, review: review) {}
                        .padding(14).frame(width: 340).frame(maxHeight: .infinity, alignment: .top).background(SwiftUI.Color(nsColor: .windowBackgroundColor))
                        .environment(\.controlActiveState, .key)),
                    "team-approve-\(name)-\(appearance)", NSSize(width: 340, height: 520))
            }
            try snapshot(AnyView(DecisionCard(decision: firstStart(now: now), asker: "Portenv") { _, _ in }
                    .padding(14).frame(width: 340).frame(maxHeight: .infinity, alignment: .top).background(SwiftUI.Color(nsColor: .windowBackgroundColor))
                        .environment(\.controlActiveState, .key)),
                "team-first-start-\(appearance)", NSSize(width: 340, height: 260))
            // A question with choices (CC-0501): one button each, no
            // default, and Decline…, in the narrowest column.
            try snapshot(AnyView(DecisionCard(decision: choices(now: now), asker: "Builder") { _, _ in }
                    .padding(14).frame(width: 260).frame(maxHeight: .infinity, alignment: .top).background(SwiftUI.Color(nsColor: .windowBackgroundColor))
                        .environment(\.controlActiveState, .key)),
                "team-decision-choices-\(appearance)", NSSize(width: 260, height: 420))
        }
    }

    static let leadFile = """
        ---
        name: Lead
        role: Splits the work and hands it out
        runner: claude --continue
        grant:
          panes: ask
          max_panes: 2
          message: [builder, reviewer]
        ---
        Split each task in two and hand the halves to Builder and Reviewer.

        """

    static let approvalAgent = TeamAgent(id: "lead", name: "Lead", status: .stopped, approved: false, runner: "claude --continue",
                                         grant: TeamGrant(panes: .ask, maxPanes: 2, message: ["builder", "reviewer"]))

    static func approvals() -> [(String, AgentFileReview)] {
        func review(_ content: String, approved: String) -> AgentFileReview? {
            let sha = SHA256Text.of(content)
            return AgentFileReview(agentID: "lead", file: AgentFileContent(content: content, sha256: sha, approvedContent: approved, runner: "claude --continue"))
        }
        let before = leadFile.replacingOccurrences(of: "max_panes: 2", with: "max_panes: 1")
        return [("new", review(leadFile, approved: "")), ("changed", review(leadFile, approved: before))].compactMap { n, r in r.map { (n, $0) } }
    }

    static func choices(now: Date) -> TeamDecision {
        TeamDecision(id: "d-choices", from: "builder", kind: .askOwner, summary: "The payments tests need a date library. What now?",
                     detail: "The tests fail on the date parsing. I can wait, install the library the API uses, or skip those tests for now.",
                     createdAt: now.addingTimeInterval(-120),
                     choices: ["Option a: wait for the API team", "Option b: install deps (date-fns 4.1, as the API uses)", "Option c: skip the date tests for now"])
    }

    static func firstStart(now: Date) -> TeamDecision {
        TeamDecision(id: "d-start", from: "portenv", kind: .teamStart, summary: "Start this workspace\u{2019}s agent team?",
                     detail: "Each agent runs as its own user in this workspace, with its own terminals and files, and asks you before it opens a pane in your window. Agents can change your project\u{2019}s files. Check their changes before you run them.",
                     createdAt: now)
    }
}
