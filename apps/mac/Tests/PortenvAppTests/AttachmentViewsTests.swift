// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

@MainActor
private final class Frames {
    var all: [String: CGRect] = [:]
}

/// Attachments in the app's views (ADR 0030 point 5): chips on messages and
/// on the decision card (above its buttons, never hiding the question), the
/// Attachments panel, and all of it fitting the narrowest column.
@MainActor
@Suite(.serialized)
struct AttachmentViewsTests {
    static let width = BoxWindowColumns<EmptyView>.inspectorWidth.min
    static let shot = TeamAttachment(id: "01JAB000000000000000000001", name: "login-390.png", mediaType: "image/png", size: 1_200_000,
                                     sha256: String(repeating: "ab", count: 32), width: 390, height: 844)
    static let plan = TeamAttachment(id: "01JAB000000000000000000002", name: String(repeating: "a-very-long-name-", count: 6) + ".md",
                                     mediaType: "text/markdown", size: 14000, sha256: String(repeating: "cd", count: 32))

    func team() -> TeamController {
        _ = NSApplication.shared
        let t = TeamController(box: "acme", daemon: SampleDaemon(savedAt: Date()))
        t.receive(.agent(TeamAgent(id: "builder", name: "Builder", status: .running, approved: true, runner: "claude")))
        t.receive(.agent(TeamAgent(id: "reviewer", name: "Reviewer", status: .idle, approved: true, runner: "claude")))
        t.receive(.message(TeamMessage(id: "1", from: "builder", to: "owner", text: "attached login-390.png", createdAt: Date(), attachments: [Self.shot])))
        t.receive(.message(TeamMessage(id: "2", from: "builder", to: "reviewer", text: "Review", createdAt: Date(), attachments: [Self.plan, Self.shot])))
        return t
    }

    func fits(_ view: some View, height: CGFloat = 100_000) {
        let host = NSHostingController(rootView: view)
        let fit = host.sizeThatFits(in: CGSize(width: Self.width, height: height))
        #expect(fit.width <= Self.width, "wants \(fit.width) pt wide in \(Self.width) pt")
    }

    @Test func chipsFitTheNarrowestColumn() {
        let t = team()
        fits(AttachmentChips(attachments: [Self.plan, Self.shot], from: "Builder", team: t))
        fits(TeamColumn(agent: t.state.agent("builder")!, team: t), height: 600)
        fits(TeamActivityColumn(team: t), height: 600)
        fits(AttachmentsColumn(team: t), height: 600)
    }

    /// The decision card's chips sit with the evidence, above the buttons;
    /// they never replace the question or Approve and Decline.
    @Test func theDecisionCardsChipsSitAboveItsButtons() async {
        let t = team()
        let d = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "Ship the redesign?", detail: "Ship the redesign?",
                             state: .pending, createdAt: Date(), attachments: [Self.plan, Self.shot])
        let frames = Frames()
        let card = DecisionCard(decision: d, asker: "Builder", team: t) { _, _ in }
            .padding(14).frame(width: Self.width)
            .coordinateSpace(name: ControlFrames.space)
            .onPreferenceChange(ControlFrames.self) { frames.all = $0 }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: Self.width, height: 700), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: card.frame(maxHeight: .infinity, alignment: .top))
        window.makeKeyAndOrderFront(nil)
        defer { window.close() }
        for _ in 0 ..< 8 { try? await Task.sleep(for: .milliseconds(20)) }
        guard let chips = frames.all[AttachmentChips.frameName], let approve = frames.all[DecisionCard.approveFrame],
              let question = frames.all[DecisionCard.questionFrame] else {
            Issue.record("frames: \(frames.all.keys.sorted())")
            return
        }
        #expect(question.maxY <= chips.minY, "the chips cover the question")
        #expect(chips.maxY <= approve.minY, "the chips aren't above Approve")
    }

    /// The panel lists every attachment, agent to agent included, newest
    /// first; muting hides none of them.
    @Test func thePanelListsEveryAttachmentWhateverIsMuted() {
        let t = team()
        t.setMuted(true, agent: "builder")
        let rows = AttachmentsColumn.rows(t, filter: "")
        #expect(rows.count == 3)
        #expect(AttachmentsColumn.rows(t, filter: "reviewer").count == 2)
        t.setMuted(false, agent: "builder")
    }

    /// ⌥⌘A shows the panel: View › Show Attachments.
    @Test func theMenuItemIsOptionCommandA() {
        #expect(AttachmentsCommand.shortcut == KeyboardShortcut("a", modifiers: [.command, .option]))
        #expect(AttachmentsCommand.title == "Show Attachments")
    }
}
