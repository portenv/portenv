// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// View › Show Sidebar (⌘0): the Team sidebar, shown by default when the
/// workspace has a team and hidden otherwise, the person's choice
/// remembered per workspace (R-0240).
struct TeamSidebarToggle: Commands {
    let box: String?
    let team: TeamController?

    static func key(_ box: String) -> String { "teamSidebar.\(box)" }

    /// Whether the window shows the sidebar: the person's choice, or by
    /// default whether there's a team; never for a portenvd without teams.
    @MainActor
    static func shows(choice: String, team: TeamController?) -> Bool {
        guard let team, team.supported else { return false }
        switch choice {
        case "shown": return true
        case "hidden": return false
        default: return team.state.hasTeam
        }
    }

    var body: some Commands {
        CommandGroup(before: .sidebar) {
            if let box, let team {
                SidebarMenuItem(key: Self.key(box), team: team).id(box)
            } else {
                Button("Show Sidebar") {}
                    .keyboardShortcut("0", modifiers: .command)
                    .disabled(true)
            }
        }
    }
}

private struct SidebarMenuItem: View {
    @AppStorage private var choice: String
    let team: TeamController

    init(key: String, team: TeamController) {
        _choice = AppStorage(wrappedValue: "", key)
        self.team = team
    }

    var body: some View {
        let shown = TeamSidebarToggle.shows(choice: choice, team: team)
        Button(shown ? "Hide Sidebar" : "Show Sidebar") { choice = shown ? "hidden" : "shown" }
            .keyboardShortcut("0", modifiers: .command)
            .disabled(!team.supported)
    }
}

/// The selected agent's column, in the inspector's place (R-0240, the
/// team-agent-selected mockup): who it runs as, its messages with you as
/// plain text, what it asks you, then its file to approve and Start
/// (R-0285). Message and its files come with the column's next part.
struct TeamColumn: View {
    let agent: TeamAgent
    @Bindable var team: TeamController

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                TeamAvatar(agent: agent, team: team.state.agents, size: 26)
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: agent.name).font(.system(size: 13, weight: .semibold))
                    Text(verbatim: TeamText.runsAs(agent)).font(.system(size: 11)).foregroundStyle(.secondary)
                    // Its routines, or why they're off (R-0258).
                    if let routines = TeamText.routinesLine(agent) {
                        Text(verbatim: routines).font(.system(size: 11)).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .accessibilityElement(children: .combine)
            Divider()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 10) {
                        ForEach(team.state.conversation(with: agent.id)) { m in
                            TeamMessageRow(message: m, sender: team.state.agent(m.from), team: team.state.agents)
                                .id(m.id)
                        }
                        // What it asks you, and the team's first start
                        // while it names this agent, answered here
                        // (GUIDELINES 6.2). Inside the scroll: a long card
                        // never sets the column's (and window's) height.
                        ForEach(team.decisions(for: agent.id)) { d in
                            DecisionCard(decision: d, asker: TeamText.asker(d, team: team.state.agents)) { approve, note in
                                Task { await team.answer(d, approve: approve, note: note) }
                            }
                            .id(d.id)
                        }
                    }
                    .padding(14)
                }
                .defaultScrollAnchor(.bottom)
                .onChange(of: (team.decisions(for: agent.id).last?.id ?? team.state.conversation(with: agent.id).last?.id)) { _, id in
                    if let id { proxy.scrollTo(id, anchor: .bottom) }
                }
            }
            Divider()
            ScrollView { TeamControls(agent: agent, team: team) }
                .frame(maxHeight: agent.approved ? 120 : 460)
                .fixedSize(horizontal: false, vertical: agent.approved)
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }
}

/// One message: who sent it, when, and its text, as plain text.
struct TeamMessageRow: View {
    let message: TeamMessage
    /// The agent that sent it; nil for you and for Portenv.
    let sender: TeamAgent?
    let team: [TeamAgent]

    var body: some View {
        let you = message.from == TeamText.owner
        let name = you ? TeamText.you : sender?.name ?? "Portenv"
        HStack(alignment: .top, spacing: 8) {
            if let sender, !you {
                TeamAvatar(agent: sender, team: team)
            } else if you {
                TeamAvatar()
            } else {
                // Portenv's own notices.
                Image(systemName: "info.circle").frame(width: 22, height: 22).foregroundStyle(.secondary)
            }
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(verbatim: name).font(.system(size: 12, weight: .semibold))
                    if let at = message.createdAt {
                        Text(at, style: .time).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
                Text(verbatim: message.text).font(.system(size: 13)).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }
}
