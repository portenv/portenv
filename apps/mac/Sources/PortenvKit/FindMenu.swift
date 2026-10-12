// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Edit › Find (R-0119): the standard submenu, sent to whatever has the
/// focus. The Acknowledgements window's text view and the terminal both
/// answer it.
public enum FindMenu {
    public static let menu = "Find"
    public static let find = "Find…"
    public static let findNext = "Find Next"
    public static let findPrevious = "Find Previous"
    public static let useSelection = "Use Selection for Find"

    /// The sender of a performTextFinderAction(_:): a menu item whose tag is
    /// the action, as for a menu item AppKit builds itself (SwiftTerm's
    /// terminal view reads it only from an NSMenuItem).
    @MainActor
    public static func sender(_ action: NSTextFinder.Action) -> NSMenuItem {
        let item = NSMenuItem()
        item.tag = action.rawValue
        return item
    }
}
