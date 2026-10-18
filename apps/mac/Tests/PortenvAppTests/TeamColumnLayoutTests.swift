// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// The agent's column with the team's first-start decision in it (R-0298):
/// the card, whose text is long, scrolls with the conversation, so it never
/// sets the column's height, nor through the inspector the window's. Shown
/// below the conversation with a fixed size, it made the column taller
/// than a small window could hold, the likeliest cause of the window that
/// went blank when the decision first appeared.
@MainActor
@Suite(.serialized)
struct TeamColumnLayoutTests {
    static let detail = "Each agent runs as its own user in this workspace, with its own terminals and files, and asks you before it opens a pane in your window. "
        + "Agents can pass through your home folder to reach the project (it becomes 0711); your logins and private folders stay yours, and Stop Team doesn\u{2019}t change it back. "
        + "Agents can change your project\u{2019}s files. Check their changes before you run them."

    @Test func aLongDecisionNeverSetsTheColumnsHeight() {
        _ = NSApplication.shared
        let team = TeamController(box: "acme", daemon: SampleDaemon(savedAt: Date()))
        let lead = TeamAgent(id: "lead", name: "Lead", status: .stopped, approved: true, runner: "claude")
        team.receive(.agent(lead))
        team.receive(.decision(TeamDecision(id: "d-start", from: "portenv", kind: .teamStart,
                                            summary: "Start this workspace\u{2019}s agent team?", detail: Self.detail,
                                            startAgents: ["lead"])))
        #expect(team.decisions(for: "lead").count == 1)
        let host = NSHostingController(rootView: TeamColumn(agent: lead, team: team))
        for height in [CGFloat(200), 260] {
            let fit = host.sizeThatFits(in: CGSize(width: BoxWindowColumns<EmptyView>.inspectorWidth.min, height: height))
            #expect(fit.height <= height, "the column wants \(fit.height) pt in \(height) pt")
        }
    }
}
