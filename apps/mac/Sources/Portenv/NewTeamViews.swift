// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

// New workspace's Team and Review and approve steps (ADR 0025 §3, §4; the
// new-box-team board). Every agent file is shown as plain text, exactly
// the bytes portenvd will write; nothing here starts an agent.

/// An agent's avatar in the plan: its colour (GUIDELINES §11) and initial.
struct PlannedAvatar: View {
    let agent: PlannedAgentFile
    let index: Int
    var size: CGFloat = 22

    var body: some View {
        TeamAvatar(look: .planned(agent, index: index), size: size)
    }
}

/// The step names at the top of New workspace with a team step.
struct StepNames: View {
    let steps: [(String, Bool)] // name, current

    var body: some View {
        HStack(spacing: 6) {
            ForEach(Array(steps.enumerated()), id: \.offset) { i, s in
                if i > 0 { Text(verbatim: "\u{00B7}").foregroundStyle(.tertiary) }
                Text(s.0).fontWeight(s.1 ? .semibold : .regular).foregroundStyle(s.1 ? .primary : .secondary)
            }
        }
        .font(.callout)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(steps.first { $0.1 }.map { "Step: \($0.0)" } ?? "")
    }
}

/// The Team step: None or a built-in team, then each agent's row.
struct TeamStepView: View {
    @Bindable var team: NewTeamFlow
    @State private var confirmingRaise = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            // Wrapped into balanced rows at the window's width: never wider
            // than the window (CardColumns).
            CardRows {
                TemplateCard(title: NewTeamText.noneTitle, line: NewTeamText.noneLine, selected: team.template == nil) {
                    Task { await team.choose(nil) }
                }
                ForEach(team.templates) { t in
                    TemplateCard(title: t.name, line: t.description, selected: team.template == t.id) {
                        Task { await team.choose(t.id) }
                    }
                }
            }
            if team.template != nil {
                // As tall as its rows: the step scrolls, as a whole, only
                // when the window can't hold them (FirstRunView).
                VStack(spacing: 0) {
                    HStack {
                        Text("Agent").frame(maxWidth: .infinity, alignment: .leading).padding(.leading, 30)
                        Text("Runner").frame(width: 140, alignment: .leading)
                        Text("Model").frame(width: 170, alignment: .leading)
                    }
                    .font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                    .padding(.horizontal, 10).padding(.vertical, 6)
                    ForEach(Array(team.agents.enumerated()), id: \.element.id) { i, a in
                        Divider()
                        AgentChoiceRow(team: team, agent: a, index: i)
                    }
                }
                .background(RoundedRectangle(cornerRadius: 8).stroke(SwiftUI.Color.primary.opacity(0.12)))
                if let raise = team.raise, let title = raise.raiseTitle {
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange).accessibilityHidden(true)
                        Text(verbatim: raise.line).fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 8)
                        Button(title) { confirmingRaise = true }
                            .accessibilityLabel("Raise Agents allowed to \(raise.raiseTo)")
                    }
                    .font(.callout)
                    .alert(raise.confirmTitle, isPresented: $confirmingRaise) {
                        Button("Cancel", role: .cancel) {}
                        Button(raise.confirmButton) { Task { await team.raiseLimit() } }
                    } message: {
                        Text(raise.confirmMessage)
                    }
                }
                VStack(alignment: .leading, spacing: 4) {
                    Text(NewTeamText.runnerOnly)
                    Text(NewTeamText.signIn)
                }
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            }
        }
        .task { if team.templates.isEmpty { await team.load() } }
    }
}

/// Choice cards in balanced rows that fit the width they're given
/// (CardColumns): never wider, however narrow the window.
struct CardRows: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache _: inout ()) -> CGSize {
        let width = Self.width(proposal, cards: subviews.count)
        let heights = rowHeights(width: width, subviews: subviews)
        return CGSize(width: width, height: heights.reduce(0, +) + CGFloat(max(0, heights.count - 1)) * CardColumns.spacing)
    }

    func placeSubviews(in bounds: CGRect, proposal _: ProposedViewSize, subviews: Subviews, cache _: inout ()) {
        let columns = CardColumns.count(cards: subviews.count, width: bounds.width)
        let column = CardColumns.columnWidth(columns: columns, width: bounds.width)
        var y = bounds.minY
        for (row, height) in rowHeights(width: bounds.width, subviews: subviews).enumerated() {
            for i in row * columns..<min(subviews.count, (row + 1) * columns) {
                let x = bounds.minX + CGFloat(i - row * columns) * (column + CardColumns.spacing)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: column, height: height))
            }
            y += height + CardColumns.spacing
        }
    }

    /// The width offered, or, with none, every card in one row at its minimum.
    private static func width(_ proposal: ProposedViewSize, cards: Int) -> CGFloat {
        if let w = proposal.width, w.isFinite { return w }
        let n = CGFloat(min(max(1, cards), CardColumns.maximum))
        return n * CardColumns.minimumWidth + (n - 1) * CardColumns.spacing
    }

    /// Each row's height: its tallest card at the column's width.
    private func rowHeights(width: CGFloat, subviews: Subviews) -> [CGFloat] {
        let columns = CardColumns.count(cards: subviews.count, width: width)
        let column = CardColumns.columnWidth(columns: columns, width: width)
        return stride(from: 0, to: subviews.count, by: columns).map { start in
            subviews[start..<min(subviews.count, start + columns)]
                .map { $0.sizeThatFits(ProposedViewSize(width: column, height: nil)).height }.max() ?? 0
        }
    }
}

/// One team in the picker.
struct TemplateCard: View {
    let title: String
    let line: String
    let selected: Bool
    let choose: () -> Void

    var body: some View {
        Button(action: choose) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.headline)
                Text(line).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(10)
            // Every card in a row as tall as the row (CardRows).
            .frame(maxWidth: .infinity, minHeight: 62, maxHeight: .infinity, alignment: .topLeading)
            .contentShape(Rectangle())
            .background(RoundedRectangle(cornerRadius: 8).fill(selected ? Color.accentColor.opacity(0.12) : Color.clear))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(selected ? Color.accentColor : Color.secondary.opacity(0.3), lineWidth: selected ? 2 : 1))
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(title). \(line)")
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }
}

/// An agent's row: include, name (its id under it), runner and model.
struct AgentChoiceRow: View {
    @Bindable var team: NewTeamFlow
    let agent: PlannedAgentFile
    let index: Int
    @FocusState private var naming: Bool

    var body: some View {
        let name = Binding(get: { team.typedName(agent.id) }, set: { v in Task { await team.rename(agent.id, v) } })
        HStack(alignment: .center, spacing: 8) {
            Toggle("", isOn: Binding(get: { agent.included }, set: { v in Task { await team.setIncluded(agent.id, v) } }))
                .toggleStyle(.checkbox).labelsHidden()
                .accessibilityLabel("Include \(agent.name)")
            PlannedAvatar(agent: agent, index: index)
            VStack(alignment: .leading, spacing: 2) {
                // Looks editable at rest, not only on hover: a field's
                // background and border, the accent while typing.
                TextField("Name", text: name)
                    .textFieldStyle(.plain)
                    .focused($naming)
                    .padding(.horizontal, 6).padding(.vertical, 3)
                    .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color(nsColor: .textBackgroundColor)))
                    .overlay(RoundedRectangle(cornerRadius: 6)
                        .strokeBorder(naming ? Color.accentColor : SwiftUI.Color.secondary.opacity(0.5), lineWidth: naming ? 2 : 1))
                    .frame(maxWidth: 180)
                    .accessibilityLabel("Name for \(agent.templateName)")
                // Its id in monospace and its role, which stay the template's.
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Text(verbatim: agent.id).font(.system(size: 10, design: .monospaced))
                    if !agent.role.isEmpty {
                        Text(verbatim: "\u{00B7} " + agent.role).font(.system(size: 10))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            // MVP: Claude Code only (ADR 0025 §9).
            Menu(agent.runnerName) {
                Text("Anthropic")
                Button("\u{2713} " + agent.runnerName) {}
            }
            .frame(width: 140)
            .accessibilityLabel("Runner for \(agent.name): \(agent.runnerName)")
            VStack(alignment: .leading, spacing: 1) {
                Picker("", selection: Binding(get: { agent.model }, set: { v in Task { await team.setModel(agent.id, v) } })) {
                    ForEach(agent.models, id: \.self) { m in
                        Text(NewTeamText.model(m) + (m == agent.templateModel ? " (template)" : "")).tag(m)
                    }
                }
                .labelsHidden()
                .accessibilityLabel("Model for \(agent.name)")
                if agent.model != agent.templateModel {
                    Text("Changed from \(NewTeamText.model(agent.templateModel))").font(.system(size: 10)).foregroundStyle(.orange)
                }
            }
            .frame(width: 170, alignment: .leading)
        }
        .padding(.horizontal, 10).padding(.vertical, 6)
        .opacity(agent.included ? 1 : 0.55)
    }
}

/// Review and approve: the included agents with their state, and the one
/// selected in full.
struct ReviewStepView: View {
    @Bindable var team: NewTeamFlow
    /// The Project step's choice, summarised at the top of the list.
    var project: ProjectChoice = .empty

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            VStack(alignment: .leading, spacing: 8) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: NewTeamText.projectTitle(project)).font(.headline)
                    Text(NewTeamText.projectSummary(project)).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 8).stroke(SwiftUI.Color.primary.opacity(0.12)))
                ScrollView {
                    VStack(spacing: 2) {
                        ForEach(Array(team.included.enumerated()), id: \.element.id) { i, a in
                            ReviewListRow(agent: a, index: i, state: team.reviewState(a.id), selected: team.reviewing == a.id) {
                                team.reviewing = a.id
                            }
                        }
                    }
                }
                Spacer(minLength: 0)
                Button(NewTeamText.approveAll) { team.approveAll() }
                    .frame(maxWidth: .infinity)
                    .disabled(!team.approveAllEnabled)
                    .accessibilityHint(team.approveAllLine ?? "")
                if let line = team.approveAllLine {
                    Text(line).font(.caption).foregroundStyle(.secondary)
                }
            }
            .frame(width: 220)
            if let a = team.included.first(where: { $0.id == team.reviewing }) {
                AgentFileReviewPane(team: team, agent: a)
            } else {
                Text("Choose an agent to read its file.").foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .onAppear { if team.reviewing == nil { team.reviewing = team.included.first?.id } }
    }
}

struct ReviewListRow: View {
    let agent: PlannedAgentFile
    let index: Int
    let state: NewTeamFlow.ReviewState
    let selected: Bool
    let select: () -> Void

    var body: some View {
        Button(action: select) {
            HStack(spacing: 8) {
                PlannedAvatar(agent: agent, index: index)
                Text(verbatim: agent.name).lineLimit(1)
                Spacer(minLength: 4)
                Text(NewTeamText.state(state)).font(.caption)
                    .foregroundStyle(state == .approved ? SwiftUI.Color.green : state == .changed ? SwiftUI.Color.orange : .secondary)
            }
            .padding(.horizontal, 8).padding(.vertical, 6)
            .contentShape(Rectangle())
            .background(RoundedRectangle(cornerRadius: 6).fill(selected ? Color.accentColor.opacity(0.15) : Color.clear))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(agent.name), \(NewTeamText.state(state))")
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }
}

/// One agent's file, in full: the runner line first (code that will run),
/// what changed from the template, its grants, its instructions as plain
/// text, where it goes and its SHA-256; Leave Out and Approve, neither the
/// default. Reading the instructions to their end opens it.
struct AgentFileReviewPane: View {
    @Bindable var team: NewTeamFlow
    let agent: PlannedAgentFile

    var body: some View {
        let state = team.reviewState(agent.id)
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    Text(verbatim: agent.name).font(.headline)
                    Text(verbatim: agent.role).foregroundStyle(.secondary).lineLimit(2)
                }
                Text(NewTeamText.runs).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                Text(verbatim: agent.runnerLine)
                    .font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                    .padding(6).frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color.primary.opacity(0.06)))
                if !agent.changes.isEmpty {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(NewTeamText.changedFromTemplate).fontWeight(.semibold).foregroundStyle(.orange)
                        ForEach(agent.changes, id: \.self) { Text(verbatim: $0).font(.system(size: 12, design: .monospaced)) }
                    }
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(.orange, lineWidth: 1.5))
                }
                ForEach(agent.problems, id: \.self) { p in
                    Label(p, systemImage: "exclamationmark.triangle").foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                }
                Text(NewTeamText.itMay).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(agent.grants, id: \.self) { Text(verbatim: "\u{00B7} " + $0).fixedSize(horizontal: false, vertical: true) }
                }
                .font(.callout)
                Toggle(NewTeamText.teamStatus(agent.name), isOn: Binding(get: { agent.teamStatus }, set: { v in Task { await team.setTeamStatus(agent.id, v) } }))
                    .toggleStyle(.checkbox)
                Text(NewTeamText.instructions).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 0) {
                    Text(verbatim: TeamText.visible(agent.instructions))
                        .font(.system(size: 12)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    // Reaching the end opens it (ADR 0025 §4).
                    Color.clear.frame(height: 1)
                        .onScrollVisibilityChange(threshold: 1) { visible in if visible { team.opened(agent.id) } }
                        .id(agent.sha256)
                }
                .padding(8)
                .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color.primary.opacity(0.06)))
                Text(verbatim: NewTeamText.written(agent.path, sha: agent.sha256))
                    .font(.system(size: 10, design: .monospaced)).foregroundStyle(.secondary)
                    .textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                HStack {
                    Spacer()
                    Button(NewTeamText.leaveOut) { Task { await team.leaveOut(agent.id) } }
                        .accessibilityLabel("Leave out \(agent.name)")
                    Button(state == .approved ? NewTeamText.state(.approved) : NewTeamText.approve(agent.name)) { team.approve(agent.id) }
                        .disabled(!team.canApprove(agent.id) || state == .approved)
                        .accessibilityLabel("Approve \(agent.name)\u{2019}s file")
                }
            }
            .padding(12)
        }
        .frame(maxWidth: .infinity)
        .background(RoundedRectangle(cornerRadius: 8).stroke(SwiftUI.Color.primary.opacity(0.12)))
    }
}
