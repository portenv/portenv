// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// What the card's buttons answered.
@MainActor
private final class Answers {
    var chose: [Int] = []
    var approved = 0
    var frames: [String: CGRect] = [:]
}

/// A question with choices (CC-0501) in a real window 260 pt wide, the
/// narrowest column a decision shows in: one button per choice, in order,
/// no Approve, Decline… as before, and each button, clicked as a person
/// would, answers its own choice.
@MainActor
@Suite(.serialized)
struct DecisionChoicesTests {
    static let width: CGFloat = 260
    static let labels = [
        "Option a: wait for the API team",
        "Option b: install deps (date-fns 4.1, as the API uses, pinned) now",
        "Option c: skip",
        String(repeating: "w", count: 40) + " " + String(repeating: "m", count: 39),
    ]

    private func settle() async {
        for _ in 0..<8 { try? await Task.sleep(for: .milliseconds(20)) }
    }

    private func card(_ d: TeamDecision, _ answers: Answers) -> some View {
        DecisionCard(decision: d, asker: "Builder", choose: { answers.chose.append($0) }) { approve, _ in
            if approve { answers.approved += 1 }
        }
        .padding(14)
        .frame(width: Self.width)
        .coordinateSpace(name: ControlFrames.space)
        .onPreferenceChange(ControlFrames.self) { answers.frames = $0 }
    }

    private func host(_ d: TeamDecision, _ answers: Answers) -> NSWindow {
        _ = NSApplication.shared
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: Self.width, height: 600),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: card(d, answers).frame(maxHeight: .infinity, alignment: .top))
        window.makeKeyAndOrderFront(nil)
        return window
    }

    private func click(_ name: String, in window: NSWindow, _ answers: Answers) async -> Bool {
        await settle()
        guard let f = answers.frames[name], let content = window.contentView else { return false }
        let point = NSPoint(x: f.midX, y: content.bounds.height - f.midY)
        for type in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            guard let e = NSEvent.mouseEvent(with: type, location: point, modifierFlags: [],
                                             timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                                             context: nil, eventNumber: 0, clickCount: 1, pressure: 1) else { return false }
            window.sendEvent(e)
            await settle()
        }
        return true
    }

    @Test(arguments: [2, 3, 4])
    func eachChoiceIsItsOwnButtonAndAnswersItself(count: Int) async {
        let answers = Answers()
        let d = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "What now?", choices: Array(Self.labels.prefix(count)))
        let window = host(d, answers)
        defer { window.close() }
        await settle()
        // N buttons, in order, top to bottom, then Decline….
        let choices = (0..<count).map { answers.frames[DecisionCard.choiceFrame($0)] }
        #expect(choices.allSatisfy { $0 != nil }, "frames: \(answers.frames.keys.sorted())")
        #expect(answers.frames[DecisionCard.choiceFrame(count)] == nil)
        let ys = choices.compactMap { $0?.minY }
        #expect(ys == ys.sorted())
        let decline = answers.frames[DecisionCard.declineFrame]
        #expect(decline != nil)
        if let decline, let last = choices.last ?? nil { #expect(decline.minY >= last.maxY) }
        // Every button fits the 260 pt column, inside the card's padding.
        for f in choices.compactMap({ $0 }) {
            #expect(f.minX >= 0 && f.maxX <= Self.width, "a choice spans \(f.minX)–\(f.maxX) pt in \(Self.width) pt")
        }
        // Each click answers its own choice, never Approve.
        for i in (0..<count).reversed() {
            #expect(await click(DecisionCard.choiceFrame(i), in: window, answers))
        }
        #expect(answers.chose == Array((0..<count).reversed()))
        #expect(answers.approved == 0)
    }

    /// An 80-character label wraps, so the whole label is on screen in 260
    /// pt; the card's natural size fits the column's width.
    @Test func aLongLabelWrapsAndTheCardFits() {
        _ = NSApplication.shared
        let answers = Answers()
        let d = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "What now?", choices: [Self.labels[2], Self.labels[3]])
        let host = NSHostingController(rootView: card(d, answers))
        let fit = host.sizeThatFits(in: CGSize(width: Self.width, height: 2000))
        #expect(fit.width <= Self.width, "the card wants \(fit.width) pt")
        let view = host.view
        view.frame = NSRect(origin: .zero, size: fit)
        view.layoutSubtreeIfNeeded()
        let short = answers.frames[DecisionCard.choiceFrame(0)]
        let long = answers.frames[DecisionCard.choiceFrame(1)]
        #expect(short != nil && long != nil)
        if let short, let long { #expect(long.height > short.height, "the 80-character label didn't wrap: \(long.height) vs \(short.height)") }
    }

    /// A yes/no question keeps Approve and Decline…, with no choice buttons.
    @Test func aYesNoQuestionIsUnchanged() async {
        let answers = Answers()
        let d = TeamDecision(id: "d2", from: "builder", kind: .askOwner, summary: "May I add left-pad?")
        let window = host(d, answers)
        defer { window.close() }
        await settle()
        #expect(answers.frames[DecisionCard.choiceFrame(0)] == nil)
        #expect(answers.frames[DecisionCard.declineFrame] == nil)
    }
}
