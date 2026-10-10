// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Per-row overlays on the terminal (ADR 0017, spike question 3): a marker
/// pinned to the row where something happened (a prompt, a command's start
/// or end) that stays on that row while the person scrolls, output streams
/// and the window resizes.
///
/// Rows are the terminal's scroll-invariant rows: counted from the first
/// line ever written, so they don't move when old lines are trimmed off the
/// top of the scrollback (the first valid row moves up instead). A resize
/// can reflow lines, which moves rows; each mark also keeps the identity of
/// its line, and a mark whose row no longer holds that line is found again
/// by searching outward for it.
public struct OverlayRows: Sendable {
    public enum Kind: Sendable, Equatable {
        case prompt
        case commandStart
        case commandEnd(exit: Int?)
    }

    public struct Mark: Sendable, Equatable {
        public fileprivate(set) var row: Int
        public let line: ObjectIdentifier
        public let kind: Kind
    }

    /// Where a mark shows: its row on screen (0 is the top visible row).
    public struct Placed: Equatable, Sendable {
        public let mark: Mark
        public let screenRow: Int
    }

    public private(set) var marks: [Mark] = []

    /// How far a lost mark is searched for after a reflow, in rows each way.
    public static let searchRadius = 400

    public init() {}

    public mutating func add(row: Int, line: ObjectIdentifier, kind: Kind) {
        // One mark of each kind per line: a redrawn prompt doesn't stack.
        marks.removeAll { $0.line == line && $0.kind == kind }
        marks.append(Mark(row: row, line: line, kind: kind))
    }

    public mutating func removeAll() { marks.removeAll() }

    /// Brings every mark up to date with the buffer: drops marks whose rows
    /// were trimmed away, and finds marks whose line moved (a reflow).
    /// `lineAt` gives the identity of the line at a scroll-invariant row, or
    /// nil outside `valid`.
    public mutating func resolve(valid: ClosedRange<Int>, lineAt: (Int) -> ObjectIdentifier?) {
        marks = marks.compactMap { mark in
            if mark.row < valid.lowerBound { return nil }
            if mark.row <= valid.upperBound, lineAt(mark.row) == mark.line { return mark }
            for d in 1 ... Self.searchRadius {
                for r in [mark.row - d, mark.row + d] where valid.contains(r) {
                    if lineAt(r) == mark.line {
                        var moved = mark
                        moved.row = r
                        return moved
                    }
                }
            }
            return nil
        }
    }

    /// The marks on screen, given the scroll-invariant row of the top
    /// visible line and the number of rows shown.
    public func visible(top: Int, rows: Int) -> [Placed] {
        marks.compactMap { m in
            let s = m.row - top
            return (0 ..< rows).contains(s) ? Placed(mark: m, screenRow: s) : nil
        }
    }

    /// The top of a screen row, in points from the top of the terminal.
    public static func y(screenRow: Int, cellHeight: CGFloat) -> CGFloat {
        CGFloat(screenRow) * cellHeight
    }

    /// The last valid scroll-invariant row, found by binary search: valid
    /// rows are contiguous from `firstValid`, and `isValid` is all a
    /// terminal exposes publicly about where its buffer ends.
    public static func lastValid(firstValid: Int, isValid: (Int) -> Bool) -> Int {
        guard isValid(firstValid) else { return firstValid - 1 }
        var lo = firstValid, step = 1
        while isValid(lo + step) { lo += step; step *= 2 }
        var hi = lo + step
        while hi - lo > 1 {
            let mid = (lo + hi) / 2
            if isValid(mid) { lo = mid } else { hi = mid }
        }
        return lo
    }

    /// The scroll-invariant row of the cursor: the live screen is always the
    /// last `rows` lines, and `cursorY` is the cursor's row on it.
    public static func cursorRow(lastValid: Int, rows: Int, cursorY: Int) -> Int {
        lastValid - (rows - 1) + cursorY
    }
}
