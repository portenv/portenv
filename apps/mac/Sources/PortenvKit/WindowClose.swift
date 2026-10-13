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

    /// The window File › Close acts on: the key window, else the main
    /// window, else the frontmost one (front to back, visible windows that
    /// can be main). With Portenv behind, or the menu driven through
    /// accessibility, there's no key window and the command still works
    /// (R-0061).
    public static func target<Window: AnyObject>(key: Window?, main: Window?, frontToBack: [Window]) -> Window? {
        key ?? main ?? frontToBack.first
    }

    /// The alert's text when the box couldn't be saved.
    public static func message(box: String) -> String {
        "\(box) couldn't be saved before closing. Its work is still on this Mac."
    }

    public static let informative = "Try again, or close now: the workspace stays as it is and is saved first thing the next time it opens."
    public static let anywayButton = "Close Anyway"

    /// Closes the box, asking on failure, then shows the list. Returns true
    /// once the list is shown, false when the person chose to stay.
    /// `closing` shows that the box is closing while it saves (true before
    /// the close, false when the person stays in the box).
    public static func run(
        _ flow: QuitFlow,
        ask: @Sendable (String) async -> QuitChoice,
        closing: @Sendable (Bool) async -> Void = { _ in },
        showList: @Sendable () async -> Void
    ) async -> Bool {
        await closing(true)
        guard await flow.run(message: message(box: flow.box), ask: ask) else {
            await closing(false)
            return false
        }
        await showList()
        return true
    }
}
