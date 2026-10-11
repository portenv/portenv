// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Asks before a window closes (WindowClose) without touching the window's
/// delegate. SwiftUI owns the delegate and sizes its content through it on
/// screen changes, Fill and full screen; standing in front of it left the
/// content at the old screen's size on a second display (R-0077–R-0080).
/// Instead, the close button's action comes here, and File › Close Tab ⌘W
/// (TabCommands) calls `requestClose`; the File menu has no other Close.
@MainActor
public final class WindowCloseHook: NSObject {
    private static let hooks = NSMapTable<NSWindow, WindowCloseHook>.weakToStrongObjects()

    /// Whether the window may close now; false keeps it open.
    public var shouldClose: @MainActor (NSWindow) -> Bool
    private weak var window: NSWindow?

    private init(window: NSWindow, shouldClose: @escaping @MainActor (NSWindow) -> Bool) {
        self.window = window
        self.shouldClose = shouldClose
    }

    /// The window's hook, made the first time. Installing again only takes
    /// the new answer and points the close button back here.
    @discardableResult
    public static func install(on window: NSWindow, shouldClose: @escaping @MainActor (NSWindow) -> Bool) -> WindowCloseHook {
        if let hook = hooks.object(forKey: window) {
            hook.shouldClose = shouldClose
            hook.attach()
            return hook
        }
        let hook = WindowCloseHook(window: window, shouldClose: shouldClose)
        hooks.setObject(hook, forKey: window)
        hook.attach()
        // AppKit may rebuild the title bar (full screen, key changes):
        // point the close button here again.
        for name in [NSWindow.didBecomeKeyNotification, NSWindow.didEnterFullScreenNotification,
                     NSWindow.didExitFullScreenNotification] {
            NotificationCenter.default.addObserver(hook, selector: #selector(windowChanged(_:)), name: name, object: window)
        }
        return hook
    }

    /// The windows with a hook, still open (to count box windows).
    public static var hookedWindows: [NSWindow] {
        (hooks.keyEnumerator().allObjects as? [NSWindow] ?? []).filter(\.isVisible)
    }

    /// Closes the window if its hook (if any) lets it, then SwiftUI's
    /// delegate. Not through performClose: that presses the close button,
    /// which comes back here.
    public static func requestClose(_ window: NSWindow) {
        if let hook = hooks.object(forKey: window), !hook.shouldClose(window) { return }
        if window.delegate?.windowShouldClose?(window) == false { return }
        window.close()
    }

    private func attach() {
        guard let button = window?.standardWindowButton(.closeButton) else { return }
        if button.target === self, button.action == #selector(closeButtonPressed(_:)) { return }
        button.target = self
        button.action = #selector(closeButtonPressed(_:))
    }

    @objc private func closeButtonPressed(_: Any?) {
        guard let window else { return }
        Self.requestClose(window)
    }

    @objc private func windowChanged(_: Notification) {
        attach()
    }
}
