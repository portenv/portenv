// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// Keeps Portenv's windows on a screen you can see (R-0044): a frame
/// restored from a display that's gone, or left behind when one is
/// unplugged, is centred on the main screen. A full-screen window is
/// macOS's to place, never moved here (CC-0500), and so is every tab of a
/// full-screen tab group (R-0408).
@MainActor
enum OnScreen {
    static func fixAll() {
        for window in NSApp.windows where window.isVisible || window.isMainWindow { fix(window) }
    }

    static func fix(_ window: NSWindow) {
        // The screen with the menu bar first.
        let screens = NSScreen.screens.map(\.visibleFrame)
        // Never a full-screen window, nor a tab of a full-screen group:
        // macOS places it (CC-0500, R-0408).
        let fullScreen = WindowPlacement.isFullScreen(
            styleSays: window.styleMask.contains(.fullScreen),
            tabsSay: window.tabbedWindows?.contains { $0.styleMask.contains(.fullScreen) } ?? false,
            frame: window.frame, screen: window.screen?.frame)
        let placed = WindowPlacement.visible(window.frame, screens: screens, fullScreen: fullScreen)
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
