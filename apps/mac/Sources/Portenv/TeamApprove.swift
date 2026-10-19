// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The agent's own controls in its column (R-0285): its file to approve,
/// with the full text, its hash and its grants; Start, off until the file
/// is approved; Stop while it runs. Portenv never starts remote agents.
struct TeamControls: View {
    let agent: TeamAgent
    @Bindable var team: TeamController

    var body: some View {
        if !agent.remote, agent.status != .invalid {
            VStack(alignment: .leading, spacing: 10) {
                if !agent.approved {
                    if let review = team.review, review.agentID == agent.id {
                        AgentFileReviewCard(agent: agent, review: review) { Task { await team.approve(review) } }
                    } else {
                        Text(TeamText.approval(agent, review: nil)).font(.system(size: 12)).foregroundStyle(.secondary)
                    }
                }
                HStack(spacing: 8) {
                    if [.running, .idle, .needsYou, .starting].contains(agent.status) {
                        Button(TeamText.stop) { Task { await team.stop(agent.id) } }
                            .accessibilityLabel("Stop \(agent.name)")
                    } else {
                        Button(TeamText.start) { Task { await team.start(agent.id) } }
                            .disabled(!team.canStart(agent))
                            .help(agent.approved ? "" : TeamText.approveFirst(agent.name))
                            .accessibilityLabel("Start \(agent.name)")
                    }
                    if agent.status == .stopped {
                        Text(TeamText.word(agent.status)).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
                // A failed Start, or why the start Approve set going
                // failed (R-0302).
                if let error = team.error ?? TeamText.startFailure(agent) {
                    Text(verbatim: error).font(.system(size: 11)).foregroundStyle(SwiftUI.Color(nsColor: .systemRed))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(14)
            // An unapproved file is fetched to show it; a changed one too.
            .task(id: agent.approved ? "" : agent.id + agent.fileSHA256) {
                if !agent.approved { await team.showFile(agent.id) }
            }
        }
    }
}

/// Approve: the agent file whole, as plain text, with what changed since
/// it was last approved, its runner on its own line (code that will run),
/// its grants and its SHA-256. Approve pins exactly this text's hash.
struct AgentFileReviewCard: View {
    let agent: TeamAgent
    let review: AgentFileReview
    let approve: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(TeamText.approveHeading(agent.name, changed: review.changed)).font(.system(size: 13, weight: .semibold))
            Text(TeamText.approval(agent, review: review)).font(.system(size: 11))
                .foregroundStyle(review.changed ? attention : .secondary)
            if !review.runner.isEmpty {
                Text(verbatim: "runner: \(review.runner)")
                    .font(.system(size: 12, weight: .semibold, design: .monospaced))
                    .textSelection(.enabled)
            }
            VStack(alignment: .leading, spacing: 2) {
                ForEach(TeamText.grants(agent.grant), id: \.self) { Text(verbatim: $0) }
            }
            .font(.system(size: 11))
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(review.diff.enumerated()), id: \.offset) { _, line in
                        Text(verbatim: Self.marker(line.kind) + TeamText.visible(line.text))
                            .foregroundStyle(Self.colour(line.kind))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .font(.system(size: 11, design: .monospaced))
                .textSelection(.enabled)
                .padding(8)
            }
            .frame(maxHeight: 220)
            .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color.primary.opacity(0.06)))
            Text(verbatim: TeamText.hashLine(review.sha256))
                .font(.system(size: 10, design: .monospaced)).foregroundStyle(.secondary)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(TeamText.approveFile, action: approve)
                    .accessibilityLabel("Approve \(agent.name)\u{2019}s file")
            }
        }
        .padding(12)
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(review.changed ? attention : SwiftUI.Color.primary.opacity(0.15), lineWidth: 1.5))
    }

    static func marker(_ k: AgentFileLine.Kind) -> String {
        switch k {
        case .same: "  "
        case .removed: "\u{2212} "
        case .added: "+ "
        }
    }

    static func colour(_ k: AgentFileLine.Kind) -> SwiftUI.Color {
        switch k {
        case .same: .primary
        case .removed: SwiftUI.Color(nsColor: .systemRed)
        case .added: SwiftUI.Color(nsColor: .systemGreen)
        }
    }
}

/// A decision (GUIDELINES 6.2): who asks, the question, its evidence,
/// Decline… (with an optional reason) and Approve, no default button, and
/// "Answered only here, by you." A question with choices (CC-0501) has
/// one button per choice, in order, in Approve's place.
struct DecisionCard: View {
    let decision: TeamDecision
    let asker: String
    /// For its attachments' chips (ADR 0030); nil shows none.
    var team: TeamController?
    /// Once answered: when, and what it's doing (R-0296).
    var answered: String?
    /// A choice, counting from 0 as listed.
    var choose: (_ index: Int) -> Void = { _ in }
    let answer: (_ approve: Bool, _ note: String) -> Void

    /// The frame names of its buttons, for the tests that click them.
    static func choiceFrame(_ index: Int) -> String { "choice-\(index)" }
    static let declineFrame = "decline"
    static let approveFrame = "approve"
    static let questionFrame = "question"
    @State private var declining = false
    @State private var reason = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(verbatim: asker == "Portenv" ? "Portenv asks you" : "\(asker) asks you")
                .font(.system(size: 11, weight: .semibold)).foregroundStyle(.secondary)
            Text(verbatim: decision.summary).font(.system(size: 13, weight: .semibold))
                .fixedSize(horizontal: false, vertical: true)
                .reportsFrame(Self.questionFrame)
            if !decision.detail.isEmpty {
                Text(verbatim: decision.detail).font(.system(size: 12)).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            // With the evidence, above the buttons: never in place of the
            // question or of Approve and Decline.
            if let team, !decision.attachments.isEmpty {
                AttachmentChips(attachments: decision.attachments, from: asker, team: team, at: decision.createdAt)
            }
            if decision.answerable && !decision.choices.isEmpty {
                // Each label is plain text, never markup; none is the
                // default (no Return shortcut).
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(decision.shownChoices.enumerated()), id: \.offset) { i, label in
                        Button { choose(i) } label: {
                            Text(verbatim: label).font(.system(size: 13))
                                .multilineTextAlignment(.leading)
                                .fixedSize(horizontal: false, vertical: true)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(ChoiceButtonStyle())
                        .accessibilityLabel(TeamText.choose(label, summary: decision.summary))
                        .reportsFrame(Self.choiceFrame(i))
                    }
                    HStack {
                        Spacer()
                        Button(TeamText.decline) { declining = true }
                            .accessibilityLabel("Decline: \(decision.summary)")
                            .reportsFrame(Self.declineFrame)
                    }
                }
            } else if decision.answerable {
                HStack {
                    Spacer()
                    Button(TeamText.decline) { declining = true }
                        .accessibilityLabel("Decline: \(decision.summary)")
                    Button(TeamText.approve) { answer(true, "") }
                        .accessibilityLabel("Approve: \(decision.summary)")
                        .reportsFrame(Self.approveFrame)
                }
            } else if let answered {
                Text(verbatim: answered).font(.system(size: 12, weight: .semibold))
                    .fixedSize(horizontal: false, vertical: true)
            }
            Text(TeamText.answeredOnlyHere).font(.system(size: 11)).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(12)
        .opacity(decision.state == .expired ? 0.5 : 1)
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(attention, lineWidth: 1.5))
        .alert("Decline?", isPresented: $declining) {
            TextField("Reason (optional)", text: $reason)
            Button("Cancel", role: .cancel) {}
            Button("Decline") { answer(false, reason); reason = "" }
        } message: {
            Text(verbatim: decision.summary)
        }
    }
}

/// A choice's button (CC-0501): full width, its label wrapping to as many
/// lines as it needs, so an 80-character label fits a narrow column.
struct ChoiceButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .padding(.horizontal, 10).padding(.vertical, 6)
            .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color.primary.opacity(configuration.isPressed ? 0.16 : 0.07)))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(SwiftUI.Color.primary.opacity(0.2), lineWidth: 1))
            .contentShape(RoundedRectangle(cornerRadius: 6))
    }
}

/// A decision Portenv asks on its own (the team's first start), selected
/// in Needs you, in the inspector's place.
struct DecisionColumn: View {
    let decision: TeamDecision
    @Bindable var team: TeamController

    var body: some View {
        ScrollView {
            DecisionCard(decision: decision, asker: TeamText.asker(decision, team: team.state.agents), team: team,
                         answered: TeamText.answered(decision, team: team.state.agents),
                         choose: { i in Task { await team.choose(decision, index: i) } }) { approve, note in
                Task { await team.answer(decision, approve: approve, note: note) }
            }
            .padding(14)
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }
}
