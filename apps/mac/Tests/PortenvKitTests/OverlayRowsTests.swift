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
}
