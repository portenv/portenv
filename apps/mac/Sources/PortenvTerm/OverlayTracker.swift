// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
import SwiftTerm

/// Follows a SwiftTerm terminal's OSC 133 marks and keeps them pinned to
/// their lines (ADR 0017, spike question 3), using only SwiftTerm's public
/// API: scroll-invariant rows (`getScrollInvariantLine`), the trimmed-line
/// count, the top visible row and the cursor's row.
public final class OverlayTracker {
    public private(set) var rows = OverlayRows()
    private let terminal: Terminal

    public init(terminal: Terminal) {
        self.terminal = terminal
        terminal.registerOscHandler(code: 133) { [weak self] payload in
            self?.osc133(String(decoding: payload, as: UTF8.self))
        }
    }

    /// The first row still in the buffer (rows above were trimmed).
    var firstValid: Int { terminal.buffer.totalLinesTrimmed }

    var lastValid: Int {
        OverlayRows.lastValid(firstValid: firstValid) { self.terminal.getScrollInvariantLine(row: $0) != nil }
    }

    /// The scroll-invariant row of the cursor.
    public var cursorRow: Int {
        OverlayRows.cursorRow(lastValid: lastValid, rows: terminal.rows, cursorY: terminal.getCursorLocation().y)
    }

    func osc133(_ payload: String) {
        // The marks belong to the main screen; a full-screen program's own
        // screen has no scrollback to pin them to.
        guard !terminal.isCurrentBufferAlternate else { return }
        let kind: OverlayRows.Kind
        switch payload.first {
        case "A": kind = .prompt
        case "C": kind = .commandStart
        case "D":
            let code = payload.split(separator: ";").dropFirst().first.flatMap { Int($0) }
            kind = .commandEnd(exit: code)
        default: return
        }
        let row = cursorRow
        guard let line = terminal.getScrollInvariantLine(row: row) else { return }
        rows.add(row: row, line: ObjectIdentifier(line), kind: kind)
    }

    /// Brings the marks up to date with the buffer (trimming, reflow).
    public func refresh() {
        let first = firstValid, last = lastValid
        guard last >= first else { rows.removeAll(); return }
        rows.resolve(valid: first ... last) { r in
            self.terminal.getScrollInvariantLine(row: r).map(ObjectIdentifier.init)
        }
    }

    /// The marks on screen now, with their screen rows. None while a
    /// full-screen program shows its own screen.
    public func placed() -> [OverlayRows.Placed] {
        refresh()
        if terminal.isCurrentBufferAlternate { return [] }
        return rows.visible(top: firstValid + terminal.buffer.yDisp, rows: terminal.rows)
    }
}
