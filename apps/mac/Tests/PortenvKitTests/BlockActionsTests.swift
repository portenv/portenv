// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A block's actions (R-0139, R-0171): Copy Output says what it couldn't
/// copy, Run Again types only the command, and asks first unless you ran it.
struct BlockActionsTests {
    let en = Locale(identifier: "en")

    @Test func copyOutputSaysWhatLeftTheScrollback() {
        #expect(BlockCopy.output(lines: ["a", "b"], trimmed: 0, locale: en) == "a\nb")
        #expect(BlockCopy.output(lines: ["z"], trimmed: 1204, locale: en)
            == "Output trimmed: the first 1,204 lines are past the scrollback.\nz")
        #expect(BlockCopy.trimmedNote(1, locale: en) == "Output trimmed: the first line is past the scrollback.")
    }

    @Test func copyBothPutsTheCommandFirst() {
        #expect(BlockCopy.both(command: "ls", output: "a b") == "$ ls\na b")
        #expect(BlockCopy.both(command: "true", output: "") == "$ true")
    }

    @Test func runAgainTypesOnlyTheCommand() {
        #expect(BlockCopy.runAgainInput("npm test -- retries") == Array("npm test -- retries\r".utf8))
        // Control characters a buffer could hold never become keystrokes.
        #expect(BlockCopy.runAgainInput("ls\u{1b}[2J\u{07} -la\u{7f}") == Array("ls[2J -la\r".utf8))
        #expect(BlockCopy.runAgainInput("  ") == nil)
        #expect(BlockCopy.runAgainInput("") == nil)
    }

    /// Only a command portenvd recorded as yours runs again on one click.
    @Test func runAgainAsksUnlessYouRanIt() {
        #expect(!BlockCopy.asksBeforeRunningAgain(authorIsYou: true))
        #expect(BlockCopy.asksBeforeRunningAgain(authorIsYou: false))
        #expect(BlockCopy.asksBeforeRunningAgain(authorIsYou: nil))
        let q = BlockCopy.runAgainQuestion(command: "npm test", author: "Grok Bot")
        #expect(q.title == "Run \u{201C}npm test\u{201D} again?")
        #expect(q.message == "Grok Bot ran it. It runs in this tab as you.")
        #expect(BlockCopy.runAgainQuestion(command: "make", author: nil).message.hasPrefix("Portenv doesn\u{2019}t know who ran it."))
    }

    /// A block that outgrew the scrollback keeps its start: the trimmed
    /// marks' rows still make the block, so its output can be counted.
    @Test func aBlockThatOutgrewTheScrollbackKeepsItsStart() {
        var rows = OverlayRows()
        let lines = (0 ..< 20).map { _ in NSObject() }
        rows.add(row: 0, line: ObjectIdentifier(lines[0]), kind: .prompt)
        rows.add(row: 1, line: ObjectIdentifier(lines[1]), kind: .commandStart)
        rows.add(row: 15, line: ObjectIdentifier(lines[15]), kind: .commandEnd(exit: 0))
        // Rows 0 to 9 trimmed: the prompt and the start go with them.
        rows.resolve(valid: 10 ... 19) { r in r >= 10 ? ObjectIdentifier(lines[r]) : nil }
        #expect(rows.marks.map(\.row) == [15])
        #expect(rows.trimmed.map(\.row) == [0, 1])
        let blocks = CommandBlocks.blocks(marks: rows.trimmed + rows.marks, cursorRow: 19) { _ in nil }
        #expect(blocks.count == 1)
        #expect(blocks.first?.output == 1 ... 14)
        #expect(blocks.first?.state == .succeeded)
        rows.removeAll()
        #expect(rows.trimmed.isEmpty)
    }
}
