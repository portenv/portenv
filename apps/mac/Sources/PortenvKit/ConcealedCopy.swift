// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Copying something secret, like the recovery key: the pasteboard says it's
/// concealed and transient (nspasteboard.org), so clipboard managers that
/// honour it don't keep it, and it's cleared after a while if it's still
/// there.
@MainActor
public enum ConcealedCopy {
    public static let concealed = NSPasteboard.PasteboardType("org.nspasteboard.ConcealedType")
    public static let transient = NSPasteboard.PasteboardType("org.nspasteboard.TransientType")
    public static let clearAfter: Duration = .seconds(60)

    /// Copies the string and returns the pasteboard's change count, to clear
    /// it later only if nothing else was copied since.
    @discardableResult
    public static func copy(_ string: String, to board: NSPasteboard = .general) -> Int {
        board.clearContents()
        board.declareTypes([.string, concealed, transient], owner: nil)
        board.setString(string, forType: .string)
        board.setString("", forType: concealed)
        board.setString("", forType: transient)
        return board.changeCount
    }

    /// Clears the pasteboard if it still holds what was copied.
    public static func clear(_ board: NSPasteboard, ifStill changeCount: Int) {
        if board.changeCount == changeCount { board.clearContents() }
    }

    /// Copies, then clears after `after` if it's still there.
    @discardableResult
    public static func copyThenClear(_ string: String, to board: NSPasteboard = .general, after: Duration = clearAfter) -> Task<Void, Never> {
        let count = copy(string, to: board)
        return Task { @MainActor in
            try? await Task.sleep(for: after)
            clear(board, ifStill: count)
        }
    }
}
