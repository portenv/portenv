// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// Keeps Portenv's windows on a screen you can see (R-0044): a frame
/// restored from a display that's gone, or left behind when one is
/// unplugged, is centred on the main screen.
@MainActor
enum OnScreen {
    static func fixAll() {
        for window in NSApp.windows where window.isVisible || window.isMainWindow { fix(window) }
    }

    static func fix(_ window: NSWindow) {
        // The screen with the menu bar first.
        let screens = NSScreen.screens.map(\.visibleFrame)
        let placed = WindowPlacement.visible(window.frame, screens: screens)
        if placed != window.frame { window.setFrame(placed, display: true) }
    }

    /// Fixes each window as it appears, and all of them when displays
    /// change.
    static func watch() {
        let center = NotificationCenter.default
        for name in [NSWindow.didBecomeKeyNotification, NSWindow.didBecomeMainNotification] {
            center.addObserver(forName: name, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { fixAll() }
            }
        }
        center.addObserver(forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { fixAll() }
        }
    }
}
