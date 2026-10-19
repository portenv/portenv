// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// The agent's column with its Team section and message field, and Team
/// activity (R-0313, R-0314): they fit the column's narrowest width, 260 pt,
/// whatever an agent writes, and the field's Return and Shift-Return do
/// what the hint says.
@MainActor
@Suite(.serialized)
struct TeamMessagesLayoutTests {
    static let width = BoxWindowColumns<EmptyView>.inspectorWidth.min
    static let hostile = "<b>bold</b> [click](https://evil.example) \u{202E}gnp.exe "
        + String(repeating: "Averyveryverylongwordwithoutanyspaces", count: 40) + "\n"
        + String(repeating: "A long line of text.\n", count: 2500)

    func team() -> TeamController {
        _ = NSApplication.shared
        let team = TeamController(box: "acme", daemon: SampleDaemon(savedAt: Date()))
        team.receive(.agent(TeamAgent(id: "lead", name: "Lead", status: .running, approved: true, runner: "claude")))
        team.receive(.agent(TeamAgent(id: "writer", name: "A Writer With A Very Long Display Name Indeed", status: .idle, runner: "claude")))
        let now = Date()
        team.receive(.message(TeamMessage(id: "1", from: "lead", to: "owner", text: "Split done.", createdAt: now)))
        team.receive(.message(TeamMessage(id: "2", from: "lead", to: "writer", text: Self.hostile, createdAt: now)))
        team.receive(.message(TeamMessage(id: "3", from: "portenv", to: "lead", kind: .notice, text: "Check the build", createdAt: now)))
        return team
    }

    func fits(_ view: some View, heights: [CGFloat] = [260, 600]) {
        let host = NSHostingController(rootView: view)
        for height in heights {
            let fit = host.sizeThatFits(in: CGSize(width: Self.width, height: height))
            #expect(fit.width <= Self.width, "wants \(fit.width) pt wide in \(Self.width) pt")
            #expect(fit.height <= height, "wants \(fit.height) pt in \(height) pt")
        }
    }

    @Test func theColumnFitsAt260WithTheTeamSectionOpenAndAHostileMessage() {
        let team = team()
        team.setDraft("line one\nline two\nline three", for: "lead")
        fits(TeamColumn(agent: team.state.agent("lead")!, team: team))
        fits(TeamSection(messages: team.state.teamMessages(of: "lead"), team: team.state.agents, open: .constant(true))
            .frame(maxHeight: .infinity), heights: [100_000])
    }

    @Test func anOpenedHostileEntryFitsAndShowsAllOnlyWhenAsked() {
        let team = team()
        let m = team.state.messages.first { $0.id == "2" }!
        // The board cut it to 8 KB; the opened entry shows a preview.
        #expect(m.text.count == 8 * 1024 && TeamMessageText.preview(m.text) != nil)
        for all in [false, true] {
            let host = NSHostingController(rootView: TeamMessageEntry(message: m, team: team.state.agents, open: true, all: all))
            let fit = host.sizeThatFits(in: CGSize(width: Self.width, height: 100_000))
            #expect(fit.width <= Self.width)
            if !all { #expect(fit.height < 400, "the preview is \(fit.height) pt tall") }
        }
    }

    @Test func teamActivityFitsAt260() {
        let team = team()
        fits(TeamActivityColumn(team: team))
        fits(TeamActivityColumn(team: team, filter: "writer"))
    }

    /// Shift-Return inserts a line break at the cursor; Return sends what
    /// the field holds, exactly.
    @Test func returnSendsAndShiftReturnBreaksTheLine() {
        _ = NSApplication.shared
        var text = ""
        var sent: [String] = []
        let field = MessageField(text: Binding(get: { text }, set: { text = $0 }), label: "Message Lead") { sent.append(text) }
        let coordinator = field.makeCoordinator()
        let tv = NSTextView()
        tv.isRichText = false
        tv.string = "first"
        tv.setSelectedRange(NSRange(location: 5, length: 0))
        coordinator.returnPressed(shift: true, in: tv)
        tv.insertText(" second ", replacementRange: tv.selectedRange())
        #expect(tv.string == "first\n second " && sent.isEmpty)
        coordinator.returnPressed(shift: false, in: tv)
        #expect(sent == ["first\n second "])
        // Return arrives as insertNewline:, which the field takes.
        #expect(coordinator.textView(tv, doCommandBy: #selector(NSResponder.insertNewline(_:))))
        #expect(!coordinator.textView(tv, doCommandBy: #selector(NSResponder.moveLeft(_:))))
    }
}
