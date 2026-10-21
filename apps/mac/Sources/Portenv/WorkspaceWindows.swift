// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Workspace windows as macOS window tabs (R-0344, R-0345; GUIDELINES
/// §3.3): a new one (⌘N, File › Open Workspace…, the tab bar's ＋) shows
/// "Your workspaces" in a new tab, ⌃⌘1–⌃⌘9 pick a tab, and ⌃Tab / ⌃⇧Tab
/// go to the next and previous one.
@MainActor
enum WorkspaceWindows {
    /// The workspace windows' WindowGroup.
    static let id = "workspace"

    /// Opens a new workspace window (set by the first window, from
    /// SwiftUI's openWindow).
    static var open: (() -> Void)?

    /// The frontmost workspace window: the main window, else the key one.
    static var front: NSWindow? {
        [NSApp.mainWindow, NSApp.keyWindow].compactMap { $0 }.first(where: isWorkspace)
    }

    static func isWorkspace(_ window: NSWindow) -> Bool {
        window.tabbingIdentifier == WindowCloseHook.workspaceTabs
    }

    /// ⌃⌘n: the nth tab in the frontmost tab group; past the last, nothing.
    static func select(number n: Int) {
        guard let front, let window = WorkspaceTabKeys.tab(number: n, in: WorkspaceTabKeys.group(of: front)) else { return }
        window.tabGroup?.selectedWindow = window
        window.makeKeyAndOrderFront(nil)
    }

    /// The workspace windows a new one may join, front to back.
    private static func hosts(for window: NSWindow) -> [NSWindow] {
        NSApp.orderedWindows.filter { $0 !== window && $0.isVisible && isWorkspace($0) && $0.tabbingMode == .preferred }
    }

    /// The frame of the group a new window would join, read before it
    /// shows (so before anything resizes the group); nil for a window of
    /// its own.
    static func groupFrame(for window: NSWindow) -> CGRect? {
        WorkspaceTabFrame.groupFrame(for: window, among: hosts(for: window), main: NSApp.mainWindow)
    }

    /// A new window that didn't join the workspace tabs by itself (made
    /// before it said it prefers tabs) joins the frontmost group, as the
    /// tab bar's ＋ would; a tab dragged out to a window of its own is the
    /// same window, already shown, and stays where it was dropped. Either
    /// way, a new tab takes the group's frame (`groupFrame`, read before it
    /// showed), never its own default size (GUIDELINES §3.3).
    static func join(_ window: NSWindow, groupFrame: CGRect?) {
        guard window.tabbingMode == .preferred, isWorkspace(window) else { return }
        if window.tabbedWindows == nil {
            let others = hosts(for: window)
            guard let host = WorkspaceTabFrame.host(for: window, among: others, main: NSApp.mainWindow) else { return }
            if let frame = WorkspaceTabFrame.joining(own: window.frame, group: groupFrame ?? host.frame,
                                                     fullScreen: host.styleMask.contains(.fullScreen)) {
                window.setFrame(frame, display: false)
            }
            host.addTabbedWindow(window, ordered: .above)
            window.makeKeyAndOrderFront(nil)
        }
        adopt(window, groupFrame: groupFrame)
    }

    /// A window in a tab group of more than one, not full screen, takes the
    /// group's frame back if its own size won (macOS joined it by itself).
    static func adopt(_ window: NSWindow, groupFrame: CGRect?) {
        guard (window.tabbedWindows?.count ?? 0) > 1,
              let frame = WorkspaceTabFrame.joining(own: window.frame, group: groupFrame,
                                                    fullScreen: window.styleMask.contains(.fullScreen)) else { return }
        window.setFrame(frame, display: true)
    }

    /// ⌃Tab / ⌃⇧Tab, seen before the terminal gets the key (it would type
    /// them): the next or previous workspace tab, as macOS's Window ›
    /// Show Next Tab and Show Previous Tab. A window with no other tab
    /// stays as it is.
    static func watchControlTab() -> Any? {
        NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            let m = event.modifierFlags
            guard let step = TabKeys.step(keyCode: event.keyCode, control: m.contains(.control), shift: m.contains(.shift),
                                          option: m.contains(.option), command: m.contains(.command)),
                  let window = event.window ?? NSApp.keyWindow, isWorkspace(window) else { return event }
            if window.tabbedWindows != nil {
                step > 0 ? window.selectNextTab(nil) : window.selectPreviousTab(nil)
            }
            return nil
        }
    }
}

/// Window › Show Workspace Tab ▸ 1–9 (⌃⌘1–⌃⌘9, R-0345), and File › Open
/// Workspace… (⌘N) in place of New Window: "Your workspaces" in a new tab.
struct WorkspaceTabCommands: Commands {
    @Environment(\.openWindow) private var openWindow
    /// Whether a workspace window is open (the commands are off without).
    let hasWindow: Bool

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            Button(WorkspaceTabText.openWorkspace) { openWindow(id: WorkspaceWindows.id) }
                .keyboardShortcut("n", modifiers: .command)
        }
        CommandGroup(before: .windowArrangement) {
            Menu(WorkspaceTabText.showWorkspaceTab) {
                ForEach(WorkspaceTabKeys.numbers, id: \.self) { n in
                    Button("\(n)") { WorkspaceWindows.select(number: n) }
                        .keyboardShortcut(KeyEquivalent(Character("\(n)")), modifiers: WorkspaceTabKeys.modifiers)
                }
            }
            .disabled(!hasWindow)
            Divider()
        }
    }
}

/// The window's tab, kept in step with its session whether the tab is in
/// front or not (a background tab's view may not update): its title
/// ("acme-api · This Mac · 2", or "New workspace" before it shows one)
/// and tooltip; and the menus' target whenever
/// the window becomes main.
struct SessionWindow: NSViewRepresentable {
    let session: WorkspaceSession
    let sessions: WorkspaceSessions

    func makeNSView(context _: Context) -> Probe { Probe(session: session, sessions: sessions) }
    func updateNSView(_: Probe, context _: Context) {}

    final class Probe: NSView {
        let session: WorkspaceSession
        let sessions: WorkspaceSessions
        private var joined = false
        private var tracking = false

        init(session: WorkspaceSession, sessions: WorkspaceSessions) {
            self.session = session
            self.sessions = sessions
            super.init(frame: .zero)
        }

        required init?(coder _: NSCoder) { nil }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            NotificationCenter.default.removeObserver(self, name: NSWindow.didBecomeMainNotification, object: nil)
            guard let window else { return }
            session.window = window
            NotificationCenter.default.addObserver(self, selector: #selector(becameMain), name: NSWindow.didBecomeMainNotification,
                                                   object: window)
            if window.isMainWindow { sessions.becameMain(window) }
            if !tracking {
                tracking = true
                track()
            } else {
                window.tab.title = session.windowTitle
            }
            if !joined {
                joined = true
                // The group's frame, before this window shows and anything
                // resizes the group (GUIDELINES §3.3).
                let groupFrame = WorkspaceWindows.groupFrame(for: window)
                DispatchQueue.main.async { [weak window, weak self] in
                    MainActor.assumeIsolated {
                        guard let window else { return }
                        WorkspaceWindows.join(window, groupFrame: groupFrame)
                        if let self { window.tab.title = self.session.windowTitle }
                    }
                }
            }
        }

        @objc private func becameMain(_ note: Notification) {
            if let window = note.object as? NSWindow { sessions.becameMain(window) }
        }

        /// Sets the tab's title now, and again whenever what it reads
        /// changes.
        private func track() {
            withObservationTracking {
                let tab = window?.tab
                tab?.title = session.windowTitle
                tab?.toolTip = session.tabHelp
            } onChange: { [weak self] in
                DispatchQueue.main.async { MainActor.assumeIsolated { self?.track() } }
            }
        }
    }
}
