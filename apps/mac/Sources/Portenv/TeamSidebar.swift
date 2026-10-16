// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The Team sidebar (GUIDELINES 6.2, the team-* mockups): Needs you, then
/// You and the agents. Everything an agent wrote is shown as plain text.
struct TeamSidebar: View {
    let state: TeamState
    /// Every tab, for the selected agent's tabs beneath its row and your
    /// tab count.
    let tabs: [TabInfo]
    @Binding var selection: TeamSelection
    var now = Date()
    /// Below 900 pt of window width: avatars and status symbols only.
    var compact = false

    static let width: CGFloat = 248
    static let compactWidth: CGFloat = 52

    var body: some View {
        List(selection: Binding(get: { Optional(selection) }, set: { selection = $0 ?? .allTabs })) {
            if !state.needsYou.isEmpty {
                Section(compact ? "" : TeamText.needsYouHeading(state.needsYou.count)) {
                    ForEach(state.needsYou) { d in
                        NeedsYouRow(decision: d, asker: state.agent(d.from), now: now, compact: compact)
                            .tag(TeamSelection.agent(d.from))
                    }
                }
            }
            // Compact: no headings, which don't fit; rows keep their labels.
            Section(compact ? "" : TeamText.team) {
                YouRow(tabs: tabs.filter { $0.agentID.isEmpty }.count, compact: compact)
                    .tag(TeamSelection.you)
                ForEach(state.agents) { a in
                    AgentRow(agent: a, team: state.agents, compact: compact)
                        .tag(TeamSelection.agent(a.id))
                    if selection == .agent(a.id), !compact {
                        ForEach(tabs.filter { $0.agentID == a.id }) { t in
                            AgentTabRow(tab: t)
                                .selectionDisabled()
                        }
                    }
                }
            }
        }
        .listStyle(.sidebar)
        // An agent's tab rows are one short line each.
        .environment(\.defaultMinListRowHeight, 20)
        .frame(width: compact ? Self.compactWidth : Self.width)
    }
}

/// An avatar, as AvatarLook says: the owner's person on grey, or an
/// agent's colour with its initial or SF Symbol.
struct TeamAvatar: View {
    let look: AvatarLook
    var size: CGFloat = 22

    /// An agent's avatar in its team.
    init(agent: TeamAgent, team: [TeamAgent], size: CGFloat = 22) {
        look = AvatarLook.agent(agent, in: team) { NSImage(systemSymbolName: $0, accessibilityDescription: nil) != nil }
        self.size = size
    }

    /// A look already decided (a tab's chip).
    init(look: AvatarLook, size: CGFloat = 22) {
        self.look = look
        self.size = size
    }

    /// The owner's.
    init(you size: CGFloat = 22) {
        look = .owner
        self.size = size
    }

    var body: some View {
        ZStack {
            Circle().fill(Self.colour(look.colour))
            if let symbol = look.symbol {
                Image(systemName: symbol).font(.system(size: size * 0.5, weight: .semibold))
            } else {
                Text(look.initial).font(.system(size: size * 0.5, weight: .semibold))
            }
        }
        .foregroundStyle(.white)
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }

    /// GUIDELINES §11's agent colours, as the system has them; grey for you.
    static func colour(_ name: String) -> SwiftUI.Color {
        switch name {
        case "indigo": .indigo
        case "teal": .teal
        case "purple": .purple
        case "pink": .pink
        case "brown": .brown
        case "mint": .mint
        default: SwiftUI.Color(nsColor: .systemGray)
        }
    }
}

/// A status as a word and a symbol; colour never carries it alone.
struct TeamStatus: View {
    let status: AgentStatus
    var symbolOnly = false

    var body: some View {
        Group {
            if symbolOnly {
                Image(systemName: TeamText.symbol(status))
            } else {
                Label(TeamText.word(status), systemImage: TeamText.symbol(status))
                    .labelStyle(StatusLabelStyle())
            }
        }
        .font(.system(size: 11, weight: status == .needsYou ? .semibold : .regular))
        .foregroundStyle(Self.tint(status))
        .lineLimit(1)
    }

    static func tint(_ s: AgentStatus) -> SwiftUI.Color {
        switch s {
        case .running: .accentColor
        case .needsYou: attention
        case .invalid: SwiftUI.Color(nsColor: .systemRed)
        case .connected: SwiftUI.Color(nsColor: .systemGreen)
        case .stopped, .starting, .idle, .disconnected: .secondary
        }
    }
}

/// GUIDELINES 11's attention colour as text: system orange in dark mode,
/// and a darker orange in light mode, where system orange is under 4.5:1
/// on the sidebar (GUIDELINES 12; the team mockups' #B25000).
let attention = SwiftUI.Color(nsColor: NSColor(name: nil) { appearance in
    appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
        ? .systemOrange
        : NSColor(srgbRed: 0xB2 / 255.0, green: 0x50 / 255.0, blue: 0, alpha: 1)
})

private struct StatusLabelStyle: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 3) {
            configuration.icon.imageScale(.small)
            configuration.title
        }
    }
}

struct AgentRow: View {
    let agent: TeamAgent
    let team: [TeamAgent]
    var compact = false

    var body: some View {
        Group {
            if compact {
                TeamAvatar(agent: agent, team: team)
                    .overlay(alignment: .bottomTrailing) { TeamStatus(status: agent.status, symbolOnly: true).offset(x: 4, y: 3) }
                    .help("\(agent.name), \(TeamText.word(agent.status))")
            } else {
                HStack(alignment: .top, spacing: 8) {
                    if agent.status == .invalid {
                        Image(systemName: "exclamationmark.triangle")
                            .foregroundStyle(SwiftUI.Color(nsColor: .systemRed))
                            .frame(width: 22, height: 22)
                    } else {
                        TeamAvatar(agent: agent, team: team)
                    }
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(spacing: 6) {
                            Text(agent.name).lineLimit(1).truncationMode(.tail)
                            Spacer(minLength: 4)
                            TeamStatus(status: agent.status)
                        }
                        let line = agent.status == .invalid ? agent.problem : agent.lastLine
                        if !line.isEmpty {
                            Text(verbatim: line).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
                        }
                    }
                }
                .padding(.vertical, 2)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(TeamText.spoken(agent))
        .accessibilityAddTraits(.isButton)
    }
}

struct YouRow: View {
    let tabs: Int
    var compact = false

    var body: some View {
        Group {
            if compact {
                TeamAvatar().help(TeamText.you)
            } else {
                HStack(spacing: 8) {
                    TeamAvatar()
                    Text(TeamText.you)
                    Spacer(minLength: 4)
                    Text(tabs == 1 ? "1 tab" : "\(tabs) tabs").font(.system(size: 11)).foregroundStyle(.secondary)
                }
                .padding(.vertical, 2)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(tabs == 1 ? "You, 1 tab" : "You, \(tabs) tabs")
        .accessibilityAddTraits(.isButton)
    }
}

/// A pending decision: who asks, the question, how long it has waited.
struct NeedsYouRow: View {
    let decision: TeamDecision
    let asker: TeamAgent?
    let now: Date
    var compact = false

    var body: some View {
        let who = asker?.name ?? decision.from
        let waited = TeamText.waiting(since: decision.createdAt, now: now)
        Group {
            if compact {
                Image(systemName: "circle.fill")
                    .foregroundStyle(attention)
                    .help("\(who): \(decision.summary)")
            } else {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: "\(who): \(decision.summary)").fontWeight(.semibold).lineLimit(2)
                    if let waited { Text(waited).font(.system(size: 11)).foregroundStyle(.secondary) }
                }
                .padding(.vertical, 4).padding(.horizontal, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(attention, lineWidth: 1.5))
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(["Needs you: \(who) asks, \(decision.summary)", waited].compactMap { $0 }.joined(separator: ". "))
        .accessibilityAddTraits(.isButton)
    }
}

/// One of the selected agent's tabs, beneath its row: "› tests · npm test".
struct AgentTabRow: View {
    let tab: TabInfo

    var body: some View {
        HStack(spacing: 4) {
            // The tab on screen: an accent dot and its name in bold
            // (R-0253).
            Circle().fill(tab.active ? SwiftUI.Color.accentColor : .clear).frame(width: 5, height: 5)
            Text(verbatim: "\u{203A} \(tab.name)").lineLimit(1).fontWeight(tab.active ? .semibold : .regular)
            if !tab.command.isEmpty {
                Text(verbatim: "\u{00B7} \(tab.command)").foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
            }
        }
        .font(.system(size: 12))
        .padding(.leading, 24)
        .accessibilityElement(children: .combine)
        .accessibilityValue(tab.active ? "on screen" : "")
    }
}
