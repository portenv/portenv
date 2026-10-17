// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Quits when one window closes, for a dev window that is the whole app.
/// Only that window: macOS opens and closes windows of its own in the app,
/// such as the text input window a click on selectable text brings, and
/// quitting on any of them closed the app on a click (R-0272, R-0273).
@MainActor
public enum QuitWithWindow {
    /// Calls `quit` once, when `window` closes. Keep the token for as long
    /// as the window should quit the app.
    @discardableResult
    public static func watch(_ window: NSWindow, quit: @escaping @MainActor () -> Void) -> NSObjectProtocol {
        NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: nil) { _ in
            MainActor.assumeIsolated { quit() }
        }
    }
}
