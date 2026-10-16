// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What closing a window does.
public enum WindowCloseAction: Equatable, Sendable {
    /// Just close it: it shows no box, or another window shows the box.
    case closeWindow
    /// Show "Your workspaces" in the window, leaving the workspace running
    /// (ADR 0023): nothing is saved, released or stopped.
    case showListLeavingItRunning
}

/// Closing the last workspace window goes back to "Your workspaces", and the
/// workspace keeps running there, with its lease, tabs and programs (ADR
/// 0023, R-0238; this replaces the owner's 2026-10-11 rule). It's closed,
/// saved and released, only by Close Workspace (`run`, below), Quit
/// (QuitAll) or Move To.
public enum WindowClose {
    public static func action(showingBox: Bool, otherBoxWindows: Int) -> WindowCloseAction {
        showingBox && otherBoxWindows == 0 ? .showListLeavingItRunning : .closeWindow
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

    /// Close Workspace: closes the box, asking on failure, then shows the
    /// list. Returns true
    /// once the list is shown, false when the person chose to stay.
    /// `closing` shows that the box is closing while it saves (true before
    /// the close, false when the person stays in the box).
    public static func run(
        _ flow: QuitFlow,
        ask: @Sendable (String) async -> QuitChoice,
        tell: @Sendable (String) async -> Void = { _ in },
        closing: @Sendable (Bool) async -> Void = { _ in },
        showList: @Sendable () async -> Void
    ) async -> Bool {
        await closing(true)
        guard await flow.run(message: message(box: flow.box), ask: ask, tell: tell) else {
            await closing(false)
            return false
        }
        await showList()
        return true
    }
}
