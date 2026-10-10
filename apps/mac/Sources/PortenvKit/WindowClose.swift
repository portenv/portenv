// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What closing a window does.
public enum WindowCloseAction: Equatable, Sendable {
    /// Just close it: it shows no box, or another window shows the box.
    case closeWindow
    /// Close the box (save and release), then show "Your boxes" in the
    /// window in place of quitting.
    case closeBoxThenShowList
}

/// Closing the last box window goes back to "Your boxes" (the box list)
/// instead of quitting (the owner, 2026-10-11); ⌘Q still quits. The box is
/// closed, saved and released first, exactly as quitting does (QuitFlow):
/// if that fails, the person is asked to try again, close anyway (the box
/// stays as it is with the quit marker and is saved first thing the next
/// time it opens) or cancel (the window stays on the box).
public enum WindowClose {
    public static func action(showingBox: Bool, otherBoxWindows: Int) -> WindowCloseAction {
        showingBox && otherBoxWindows == 0 ? .closeBoxThenShowList : .closeWindow
    }

    /// The alert's text when the box couldn't be saved.
    public static func message(box: String) -> String {
        "\(box) couldn't be saved before closing. Its work is still on this Mac."
    }

    public static let informative = "Try again, or close now: the box stays as it is and is saved first thing the next time it opens."
    public static let anywayButton = "Close Anyway"

    /// Closes the box, asking on failure, then shows the list. Returns true
    /// once the list is shown, false when the person chose to stay.
    public static func run(
        _ flow: QuitFlow,
        ask: @Sendable (String) async -> QuitChoice,
        showList: @Sendable () async -> Void
    ) async -> Bool {
        guard await flow.run(message: message(box: flow.box), ask: ask) else { return false }
        await showList()
        return true
    }
}
