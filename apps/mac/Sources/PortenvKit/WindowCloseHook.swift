// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Asks before a window closes (WindowClose) without touching the window's
/// delegate. SwiftUI owns the delegate and sizes its content through it on
/// screen changes, Fill and full screen; standing in front of it left the
/// content at the old screen's size on a second display (R-0077–R-0080).
/// Instead, the close button's action comes here, and File › Close Tab ⌘W
/// (TabCommands) calls `requestClose`; the File menu has no other Close.
/// Workspace windows are macOS window tabs (R-0344, R-0345): a native
/// tab's close button is the window's own, which comes here too. Every
/// other window keeps tabbing off.
@MainActor
public final class WindowCloseHook: NSObject {
    private static let hooks = NSMapTable<NSWindow, WindowCloseHook>.weakToStrongObjects()

    /// Whether the window may close now; false keeps it open.
    public var shouldClose: @MainActor (NSWindow) -> Bool
    private weak var window: NSWindow?
    /// What the close button did before the hook, put back when the
    /// window closes.
    private weak var originalTarget: AnyObject?
    private var originalAction: Selector?

    private init(window: NSWindow, shouldClose: @escaping @MainActor (NSWindow) -> Bool) {
        self.window = window
        self.shouldClose = shouldClose
    }

    /// The tab group workspace windows share (GUIDELINES §3.3).
    public static let workspaceTabs = "workspace"

    /// The window's hook, made the first time. Installing again only takes
    /// the new answer and points the close button back here. `tabs`: the
    /// window joins the workspace windows' tab group (a workspace window);
    /// otherwise it never becomes a tab (first run).
    @discardableResult
    public static func install(on window: NSWindow, tabs: Bool = false,
                               shouldClose: @escaping @MainActor (NSWindow) -> Bool) -> WindowCloseHook {
        setTabbing(window, tabs: tabs)
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
        // The map's weak keys don't reliably drop their values: a closed
        // window's hook goes when it closes (R-0084).
        NotificationCenter.default.addObserver(hook, selector: #selector(windowWillClose(_:)),
                                               name: NSWindow.willCloseNotification, object: window)
        return hook
    }

    /// Turns on macOS's window tabs for the app, so workspace windows group
    /// as tabs and the Window menu has Show Previous Tab (⌃⇧Tab), Show
    /// Next Tab (⌃Tab), Move Tab to New Window and Merge All Windows (R-0345).
    /// Only windows that ask for tabs (`install(on:tabs:)`) become tabs;
    /// every other window (Settings, Acknowledgements) says so itself with
    /// `.disallowed`. Call before the first window is made
    /// (applicationWillFinishLaunching).
    public static func turnOnWorkspaceTabs() {
        NSWindow.allowsAutomaticWindowTabbing = true
    }

    /// A workspace window prefers tabs, in the workspace group; any other
    /// window never becomes one.
    static func setTabbing(_ window: NSWindow, tabs: Bool) {
        if tabs {
            window.tabbingIdentifier = workspaceTabs
            window.tabbingMode = .preferred
        } else {
            window.tabbingMode = .disallowed
        }
    }

    /// The window's hook, if it has one.
    static func hook(for window: NSWindow) -> WindowCloseHook? {
        hooks.object(forKey: window)
    }

    /// The windows with a hook, still open (to count box windows): those
    /// on screen, and the tabs behind them in a tab group (a background
    /// tab isn't visible, but it's open).
    public static var hookedWindows: [NSWindow] {
        (hooks.keyEnumerator().allObjects as? [NSWindow] ?? []).filter(isOpen)
    }

    static func isOpen(_ window: NSWindow) -> Bool {
        window.isVisible || (window.tabbedWindows?.contains { $0 !== window && $0.isVisible } ?? false)
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
        originalTarget = button.target
        originalAction = button.action
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

    /// Drops this hook and gives the close button back, so a window that
    /// is shown again without a new install still closes; installing
    /// again makes a new hook.
    @objc private func windowWillClose(_: Notification) {
        withExtendedLifetime(self) {
            NotificationCenter.default.removeObserver(self)
            guard let window else { return }
            if let button = window.standardWindowButton(.closeButton), button.target === self {
                button.target = originalTarget
                button.action = originalAction
            }
            if Self.hooks.object(forKey: window) === self { Self.hooks.removeObject(forKey: window) }
        }
    }
}
