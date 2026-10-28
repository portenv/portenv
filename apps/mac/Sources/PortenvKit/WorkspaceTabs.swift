// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI

/// One workspace window's session (R-0344, R-0345; GUIDELINES §3.3): the
/// workspace it shows and everything the window holds for it (the box's
/// controller, its tabs, its team and its panes, so the selected agent, the
/// current tab and the split layout are this window's own). Each window
/// owns one; closing the window ends the session, never the workspace,
/// which keeps running in portenvd (ADR 0023).
@MainActor
@Observable
public final class WorkspaceSession {
    /// What the window shows besides a workspace.
    public enum Screen: Equatable, Sendable {
        /// The first window: the launch's own screens (the wait, first run,
        /// the list with its notice).
        case launch
        /// "Your workspaces", to pick one (a new tab, or after leaving one).
        case list
        /// New Workspace… from this window's list.
        case newBox
        case workspace
    }

    public var screen: Screen
    public private(set) var controller: BoxController?
    public private(set) var tabs: TabsController?
    public private(set) var split: SplitController?
    public private(set) var team: TeamController?
    /// New Workspace… started from this window's list.
    public var newBox: FirstRunFlow?
    /// Going back to the list (the close button on the last workspace
    /// window): one at a time.
    public var closing = false
    /// The window showing this session.
    @ObservationIgnored public weak var window: NSWindow?
    /// Opening the workspace (up to following its state): leaving waits
    /// for it, so an open in progress is never cut short.
    @ObservationIgnored public var openTask: Task<Void, Never>?
    /// What the session follows while it shows the workspace (its state,
    /// its team), cancelled when it leaves.
    @ObservationIgnored private var tasks: [Task<Void, Never>] = []

    private let makeController: @MainActor (String) -> BoxController
    private let daemon: DaemonAPI

    public init(daemon: DaemonAPI, screen: Screen = .list,
                makeController: (@MainActor (String) -> BoxController)? = nil) {
        self.daemon = daemon
        self.screen = screen
        self.makeController = makeController ?? { BoxController(box: $0, daemon: daemon) }
    }

    /// The workspace this window shows, if any.
    public var box: String? { controller?.box }

    /// Decisions waiting for you in this workspace, from the team this
    /// session watches (the sidebar's Needs you).
    public var needsYou: Int { team?.state.needsYou.count ?? 0 }

    /// The window tab's title; nil while no workspace shows.
    public var tabTitle: String? {
        controller.map { WorkspaceTabTitle.text(box: $0.box, location: $0.location, needsYou: needsYou) }
    }

    /// The tab's tooltip, spelling the count out.
    public var tabHelp: String? {
        controller.map { WorkspaceTabTitle.help(box: $0.box, location: $0.location, needsYou: needsYou) }
    }

    /// Shows a workspace in this window, with controllers of its own.
    @discardableResult
    public func show(_ box: String) -> BoxController {
        leave(to: .workspace)
        let controller = makeController(box)
        let tabs = TabsController(box: box, daemon: daemon)
        self.controller = controller
        self.tabs = tabs
        split = SplitController(tabs: tabs)
        team = TeamController(box: box, daemon: daemon)
        newBox = nil
        screen = .workspace
        return controller
    }

    /// Keeps a task that follows the workspace while it shows here.
    public func keep(_ task: Task<Void, Never>) {
        tasks.append(task)
    }

    /// Back to the list: what the window followed stops and its
    /// controllers go. The workspace keeps running (nothing is closed).
    public func leave() { leave(to: .list) }

    private func leave(to next: Screen) {
        tasks.forEach { $0.cancel() }
        tasks = []
        openTask = nil
        controller = nil
        tabs = nil
        split = nil
        team = nil
        screen = next
    }

    /// Brings this window (its tab) to the front.
    public func bringForward() {
        guard let window else { return }
        window.tabGroup?.selectedWindow = window
        window.makeKeyAndOrderFront(nil)
    }
}

/// Every open workspace window's session, and which one the menus act on.
@MainActor
@Observable
public final class WorkspaceSessions {
    public private(set) var all: [WorkspaceSession] = []
    /// The session whose window is main: the menu bar's target (Workspace,
    /// tabs, split, inspector, sidebar). A window that isn't a workspace
    /// window (Settings) leaves it as it was.
    public private(set) var current: WorkspaceSession?

    public init() {}

    public func add(_ s: WorkspaceSession) {
        guard !all.contains(where: { $0 === s }) else { return }
        all.append(s)
        if current == nil { current = s }
    }

    /// The window closed: its session stops following the workspace,
    /// which keeps running.
    public func remove(_ s: WorkspaceSession) {
        s.leave()
        all.removeAll { $0 === s }
        if current === s { current = all.last }
    }

    /// A window became main.
    public func becameMain(_ window: NSWindow) {
        if let s = all.first(where: { $0.window === window }) { current = s }
    }

    /// The session showing a workspace, if a window shows it.
    public func showing(_ box: String) -> WorkspaceSession? {
        all.first { $0.box == box }
    }

    /// Where a workspace opens when a window asks for it.
    public enum OpenChoice: Equatable {
        /// In the window that asked.
        case here
        /// Already open in another window: its tab comes forward.
        case focus(WorkspaceSession)

        public static func == (a: OpenChoice, b: OpenChoice) -> Bool {
            switch (a, b) {
            case (.here, .here): true
            case let (.focus(x), .focus(y)): x === y
            default: false
            }
        }
    }

    public func choose(_ box: String, for s: WorkspaceSession) -> OpenChoice {
        if let other = showing(box), other !== s { return .focus(other) }
        return .here
    }

    /// Every workspace shown in a window.
    public var controllers: [BoxController] { all.compactMap(\.controller) }

    /// What Quit names when portenvd doesn't answer (R-0252): what it last
    /// said was running, and every workspace open in a window here.
    public func knownRunning(also running: Set<String>) -> [String] {
        var names = running
        for c in controllers where c.isOpen { names.insert(c.box) }
        return names.sorted()
    }
}

/// A workspace tab's title (R-0344): its name, where it runs, and how many
/// decisions wait for you when any do: "acme-api · This Mac · 2".
public enum WorkspaceTabTitle {
    public static func text(box: String, location: BoxController.Location, needsYou: Int) -> String {
        join([box, place(location), needsYou > 0 ? "\(needsYou)" : nil])
    }

    /// The tooltip, with the count named: "acme-api · This Mac · Needs you · 2".
    public static func help(box: String, location: BoxController.Location, needsYou: Int) -> String {
        join([box, place(location), needsYou > 0 ? TeamText.needsYouHeading(needsYou) : nil])
    }

    static func place(_ location: BoxController.Location) -> String? {
        switch location {
        case .thisMac: "This Mac"
        case .server(let target): ServerName.display(target)
        case .closed: nil
        }
    }

    private static func join(_ parts: [String?]) -> String {
        parts.compactMap { $0 }.joined(separator: " \u{00B7} ")
    }
}

/// The words of workspace tabs (GUIDELINES §3.3).
public enum WorkspaceTabText {
    /// File › Open Workspace… (⌘N): "Your workspaces" in a new tab.
    public static let openWorkspace = "Open Workspace\u{2026}"
    /// Window › Show Workspace Tab ▸ 1–9 (⌃⌘1–⌃⌘9).
    public static let showWorkspaceTab = "Show Workspace Tab"
    /// A tab showing the list.
    public static let list = "Your workspaces"
}

/// ⌃⌘1–⌃⌘9 (R-0345): the nth workspace tab of the frontmost tab group.
/// ⌘1–⌘9 stay the terminal's tabs.
public enum WorkspaceTabKeys {
    public static let numbers = Array(1...9)
    public static let modifiers: EventModifiers = [.control, .command]

    /// The menu's shortcut text, "⌃⌘3".
    public static func shortcut(_ n: Int) -> String { "\u{2303}\u{2318}\(n)" }

    /// The nth tab (from 1); past the last tab, nothing.
    public static func tab<T>(number n: Int, in tabs: [T]) -> T? {
        tabs.indices.contains(n - 1) ? tabs[n - 1] : nil
    }

    /// A window's tab group, in the tab bar's order; a lone window is a
    /// group of one.
    @MainActor
    public static func group(of window: NSWindow) -> [NSWindow] {
        if let windows = window.tabGroup?.windows, windows.contains(where: { $0 === window }) { return windows }
        return [window]
    }
}

/// File › New Workspace… (⇧⌘N, R-0404): the same New Workspace flow as the
/// list's "New Workspace…" button, from the window in front, or the launch
/// window when none is. A window showing a workspace keeps it: the flow
/// starts in a new tab instead.
public enum NewWorkspaceCommand {
    public static let title = "New Workspace\u{2026}"
    public static let key: KeyEquivalent = "n"
    public static let modifiers: EventModifiers = [.command, .shift]
    /// The menu's shortcut text, "⇧⌘N".
    public static let shortcut = "\u{21E7}\u{2318}N"

    /// What a window shows, as far as the command cares.
    public enum Shows: Equatable, Sendable {
        /// "Your workspaces": the flow starts here.
        case list
        /// New Workspace… or first run already: it comes forward as it is.
        case creating
        /// A workspace: the flow starts in a new tab.
        case workspace
        /// The launch hasn't decided yet (or a workspace is opening): it
        /// comes forward, and its list or first run follows.
        case starting
    }

    public enum Route: Equatable, Sendable {
        case here, bringForward, newTab
    }

    /// With no window (nil), a new one.
    public static func route(_ shows: Shows?) -> Route {
        switch shows {
        case .list: .here
        case .creating, .starting: .bringForward
        case .workspace, nil: .newTab
        }
    }

    /// What a window's session shows, given the launch's decision (the
    /// launch window shows it; every window waits for it).
    @MainActor
    public static func shows(_ s: WorkspaceSession, launch: Launch?) -> Shows {
        if s.controller != nil { return .workspace }
        switch s.screen {
        case .newBox:
            return s.newBox != nil ? .creating : .list
        case .launch:
            switch launch {
            case nil, .open: return .starting
            case .firstRun: return .creating
            case .boxList: return .list
            }
        case .list, .workspace:
            return launch == nil ? .starting : .list
        }
    }

    /// Carries the command out: the window in front, else the launch
    /// window.
    @MainActor
    public static func perform(front: WorkspaceSession?, launchWindow: WorkspaceSession?, launch: Launch?,
                               start: (WorkspaceSession) -> Void, openTab: () -> Void,
                               bringForward: (WorkspaceSession) -> Void) {
        let target = front ?? launchWindow
        switch route(target.map { shows($0, launch: launch) }) {
        case .here: if let target { start(target) }
        case .bringForward: if let target { bringForward(target) }
        case .newTab: openTab()
        }
    }
}
