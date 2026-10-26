// SPDX-License-Identifier: Apache-2.0

import Foundation
@testable import PortenvKit
import Testing

/// A fake buffer: line objects at scroll-invariant rows, from `first`.
private final class Line {}

private struct FakeBuffer {
    var first = 0
    var lines: [Line]
    func lineAt(_ r: Int) -> ObjectIdentifier? {
        let i = r - first
        return lines.indices.contains(i) ? ObjectIdentifier(lines[i]) : nil
    }

    var valid: ClosedRange<Int> { first ... first + lines.count - 1 }
}

struct OverlayRowsTests {
    @Test func marksShowOnTheirRowsAsTheViewScrolls() {
        let buf = FakeBuffer(lines: (0 ..< 100).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 10, line: buf.lineAt(10)!, kind: .prompt)
        o.add(row: 50, line: buf.lineAt(50)!, kind: .prompt)
        o.resolve(valid: buf.valid, lineAt: buf.lineAt)
        // Top at 0, 24 rows: only row 10 shows, at screen row 10.
        #expect(o.visible(top: 0, rows: 24).map(\.screenRow) == [10])
        // Scrolled so row 40 is at the top: row 50 shows at screen row 10.
        #expect(o.visible(top: 40, rows: 24).map(\.screenRow) == [10])
        // Rows at the edges: the top row and the last row.
        #expect(o.visible(top: 10, rows: 24).map(\.screenRow) == [0])
        #expect(o.visible(top: 27, rows: 24).map(\.screenRow) == [23])
        #expect(o.visible(top: 51, rows: 24).isEmpty)
    }

    @Test func trimmingKeepsRowsAndDropsMarksThatScrolledAway() {
        var buf = FakeBuffer(lines: (0 ..< 100).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 5, line: buf.lineAt(5)!, kind: .prompt)
        o.add(row: 60, line: buf.lineAt(60)!, kind: .prompt)
        // 20 lines trimmed off the top: rows are unchanged, row 5 is gone.
        buf.first = 20
        buf.lines.removeFirst(20)
        o.resolve(valid: buf.valid, lineAt: buf.lineAt)
        #expect(o.marks.map(\.row) == [60])
    }

    @Test func aReflowedLineIsFoundAgain() {
        var buf = FakeBuffer(lines: (0 ..< 100).map { _ in Line() })
        var o = OverlayRows()
        let marked = buf.lines[70]
        o.add(row: 70, line: ObjectIdentifier(marked), kind: .prompt)
        // A narrower window wraps 12 earlier lines into two: the marked line
        // moves down 12 rows.
        buf.lines.insert(contentsOf: (0 ..< 12).map { _ in Line() }, at: 30)
        o.resolve(valid: buf.valid, lineAt: buf.lineAt)
        #expect(o.marks.map(\.row) == [82])
        // Wider again: it moves back up.
        buf.lines.removeSubrange(30 ..< 42)
        o.resolve(valid: buf.valid, lineAt: buf.lineAt)
        #expect(o.marks.map(\.row) == [70])
    }

    @Test func aMarkWhoseLineIsGoneIsDropped() {
        var buf = FakeBuffer(lines: (0 ..< 100).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 40, line: buf.lineAt(40)!, kind: .prompt)
        buf.lines[40] = Line() // the line object was replaced (cleared)
        o.resolve(valid: buf.valid, lineAt: buf.lineAt)
        #expect(o.marks.isEmpty)
    }

    @Test func aRedrawnPromptDoesntStack() {
        let buf = FakeBuffer(lines: (0 ..< 10).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 3, line: buf.lineAt(3)!, kind: .prompt)
        o.add(row: 3, line: buf.lineAt(3)!, kind: .prompt)
        o.add(row: 3, line: buf.lineAt(3)!, kind: .commandStart)
        #expect(o.marks.count == 2)
    }

    @Test func rowsAndPoints() {
        #expect(OverlayRows.y(screenRow: 0, cellHeight: 17) == 0)
        #expect(OverlayRows.y(screenRow: 3, cellHeight: 17) == 51)
        for last in [0, 1, 2, 23, 24, 1000, 1023, 10007] {
            #expect(OverlayRows.lastValid(firstValid: 0) { $0 >= 0 && $0 <= last } == last)
        }
        #expect(OverlayRows.lastValid(firstValid: 500) { $0 >= 500 && $0 <= 777 } == 777)
        #expect(OverlayRows.lastValid(firstValid: 5) { _ in false } == 4)
        // 24 rows, last valid 1023: the live screen is 1000-1023.
        #expect(OverlayRows.cursorRow(lastValid: 1023, rows: 24, cursorY: 0) == 1000)
        #expect(OverlayRows.cursorRow(lastValid: 1023, rows: 24, cursorY: 23) == 1023)
    }

    /// A command's status belongs to its own prompt row (finding 23). The
    /// shell sends D;<exit> when the next prompt starts, so the end mark is
    /// on the next prompt's row (with that prompt's A), and for a command
    /// with no output on its own start row too. The marks here are the rows
    /// bash's integration put them on: git apply (128), cd (no output),
    /// git merge (128), git config.
    @Test func eachStatusSitsOnThePromptOfTheCommandThatEnded() {
        let buf = FakeBuffer(lines: (0 ..< 20).map { _ in Line() })
        var o = OverlayRows()
        let marks: [(Int, OverlayRows.Kind)] = [
            (0, .prompt), (1, .commandStart), (2, .commandEnd(exit: 128)), (2, .prompt),
            (3, .commandStart), (3, .commandEnd(exit: 0)), (3, .prompt),
            (4, .commandStart), (6, .commandEnd(exit: 128)), (6, .prompt),
            (7, .commandStart), (8, .commandEnd(exit: 0)), (8, .prompt),
        ]
        for (row, kind) in marks { o.add(row: row, line: buf.lineAt(row)!, kind: kind) }
        let want: [OverlayRows.Status] = [
            .init(row: 0, exit: 128), .init(row: 2, exit: 0), .init(row: 3, exit: 128), .init(row: 6, exit: 0),
        ]
        #expect(o.statuses() == want)
        // Scrolled so row 2 is at the top: the statuses of rows 0 and 1 are
        // above the screen.
        let shown: [OverlayRows.Status] = [.init(row: 0, exit: 0), .init(row: 1, exit: 128), .init(row: 4, exit: 0)]
        #expect(o.visibleStatuses(top: 2, rows: 5) == shown)
    }

    /// The order marks arrived in on one row doesn't move a status: a
    /// prompt redrawn on the row of the previous command's end mark (its A
    /// added again, after the D) still follows that D.
    @Test func aRedrawnPromptDoesntTakeThePreviousStatus() {
        let buf = FakeBuffer(lines: (0 ..< 10).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 0, line: buf.lineAt(0)!, kind: .prompt)
        o.add(row: 1, line: buf.lineAt(1)!, kind: .commandStart)
        o.add(row: 3, line: buf.lineAt(3)!, kind: .prompt)
        o.add(row: 3, line: buf.lineAt(3)!, kind: .commandEnd(exit: 2))
        #expect(o.statuses() == [OverlayRows.Status(row: 0, exit: 2)])
    }

    /// An end mark with no command since the last prompt, or a command whose
    /// prompt isn't in the buffer, shows no status: never one on the wrong
    /// row.
    @Test func anEndMarkWithNoCommandOrPromptShowsNothing() {
        let buf = FakeBuffer(lines: (0 ..< 10).map { _ in Line() })
        var o = OverlayRows()
        o.add(row: 1, line: buf.lineAt(1)!, kind: .commandStart)
        o.add(row: 2, line: buf.lineAt(2)!, kind: .commandEnd(exit: 1))
        o.add(row: 2, line: buf.lineAt(2)!, kind: .prompt)
        o.add(row: 4, line: buf.lineAt(4)!, kind: .commandEnd(exit: 0))
        o.add(row: 4, line: buf.lineAt(4)!, kind: .prompt)
        #expect(o.statuses().isEmpty)
    }
}
