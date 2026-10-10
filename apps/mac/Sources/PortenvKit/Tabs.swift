// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// A tab of the box's terminal: one tmux window in the box (GUIDELINES.md
/// §3, §4.1). The ID is stable for the tab's life.
public struct TabInfo: Equatable, Sendable, Identifiable {
    public let id: String
    public var name: String
    /// The tab this app's terminal shows (each viewer has its own).
    public var active: Bool
    /// The program running in the tab's foreground, as the box agent sees
    /// it; empty when it's just the shell.
    public var program: String

    public init(id: String, name: String, active: Bool, program: String = "") {
        self.id = id
        self.name = name
        self.active = active
        self.program = program
    }
}

/// The app's viewer name: the box agent gives this app its own grouped tmux
/// session, so an agent choosing a tab never moves the app's (1.2).
public enum TabViewer {
    public static let app = "app"
}

/// ⌃Tab and ⌃⇧Tab (§4.1): the next and previous tab on any keyboard
/// layout, as in Safari and Terminal. Next is 1, previous is -1; any other
/// key, or Tab with ⌥ or ⌘, is not a tab key and returns nil.
public enum TabKeys {
    public static let tabKeyCode: UInt16 = 48

    public static func step(keyCode: UInt16, control: Bool, shift: Bool, option: Bool, command: Bool) -> Int? {
        guard keyCode == tabKeyCode, control, !option, !command else { return nil }
        return shift ? -1 : 1
    }
}

/// A close waiting for the person's answer: the tab still runs a program.
public struct PendingClose: Equatable, Sendable {
    public let id: String
    public let program: String
}

/// What closing the current tab did (⌘W).
public enum CloseResult: Equatable, Sendable {
    case closed
    /// A program still runs: `pendingClose` holds the question.
    case asking
    /// The last tab: close the window instead (it saves and releases).
    case closeWindow
    case nothing
}

/// Texts of the tab bar (GUIDELINES.md §4.1).
public enum TabText {
    /// The question before closing a tab that still runs a program.
    public static func closeQuestion(_ p: PendingClose) -> String { "Close this tab? \(p.program) is still running." }
    public static let closeButton = "Close Tab"
}

/// Tab names: the app owns them (§4.1).
public enum TabNames {
    /// The name `+` gives a new tab: "shell", then "shell 2", "shell 3"…,
    /// the first one not in use.
    public static func next(after names: [String]) -> String {
        if !names.contains("shell") { return "shell" }
        var n = 2
        while names.contains("shell \(n)") { n += 1 }
        return "shell \(n)"
    }

    /// The name as typed, trimmed; a plain problem if it can't be a tab's
    /// name (the box agent checks the same).
    public static func check(_ typed: String) -> Result<String, DaemonError> {
        let name = typed.trimmingCharacters(in: .whitespaces)
        if name.isEmpty { return .failure(DaemonError("A tab needs a name.")) }
        if name.count > 32 { return .failure(DaemonError("Tab names can be at most 32 characters.")) }
        if name.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) {
            return .failure(DaemonError("Tab names can't contain tabs or other control characters."))
        }
        return .success(name)
    }
}

/// The box's tab bar: the tabs as the box agent reports them, and the
/// actions on them. Every action goes through portenvd.
@MainActor
@Observable
public final class TabsController {
    public let box: String
    public private(set) var tabs: [TabInfo] = []
    /// The last failure, shown to the person; cleared by dismissError.
    public private(set) var error: String?
    private let daemon: DaemonAPI
    private let retry: Duration
    private let now: @Sendable () -> Date
    private var commands = CommandWatch()
    /// Called for each command that finished in a tab (its foreground
    /// program ended), with how long it ran: the app decides whether it's
    /// worth a notification (§5).
    public var commandFinished: (@MainActor (CommandWatch.Finished) -> Void)?

    public init(box: String, daemon: DaemonAPI, retry: Duration = .seconds(1),
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.box = box
        self.daemon = daemon
        self.retry = retry
        self.now = now
    }

    /// A box keeps at least one tab.
    public var canClose: Bool { tabs.count > 1 }

    /// A close waiting for the person (the tab runs a program).
    public private(set) var pendingClose: PendingClose?

    /// The tab the terminal shows.
    public var current: TabInfo? { tabs.first { $0.active } }

    /// Reads the tabs once.
    public func load() async {
        if let ts = try? await daemon.tabs(box) { tabs = ts }
    }

    /// Follows the tabs as the box agent reports them, watching again after
    /// the stream ends (the box restarted, moved, or portenvd went away).
    /// Runs until the task is cancelled.
    public func follow() async {
        while !Task.isCancelled {
            do {
                for try await ts in daemon.watchTabs(box) {
                    tabs = ts
                    for f in commands.update(ts, at: now()) { commandFinished?(f) }
                }
            } catch {}
            if Task.isCancelled { return }
            try? await Task.sleep(for: retry)
        }
    }

    /// `+`: a new tab at the end, shown at once.
    public func newTab() async {
        let name = TabNames.next(after: tabs.map(\.name))
        await perform {
            let t = try await self.daemon.newTab(self.box, name: name)
            if !self.tabs.contains(where: { $0.id == t.id }) { self.tabs.append(t) }
            self.show(t.id)
        }
    }

    /// Closes a tab, never the last one.
    public func close(_ id: String) async {
        guard canClose else { return }
        await perform {
            try await self.daemon.closeTab(self.box, id: id)
            let wasActive = self.tabs.first { $0.id == id }?.active ?? false
            self.tabs.removeAll { $0.id == id }
            if wasActive, let last = self.tabs.last { self.show(last.id) }
        }
    }

    /// Renames a tab; false (with the problem in `error`) when the name
    /// can't be used.
    @discardableResult
    public func rename(_ id: String, to typed: String) async -> Bool {
        let name: String
        switch TabNames.check(typed) {
        case .failure(let e):
            error = e.message
            return false
        case .success(let n): name = n
        }
        var ok = false
        await perform {
            try await self.daemon.renameTab(self.box, id: id, name: name)
            if let i = self.tabs.firstIndex(where: { $0.id == id }) { self.tabs[i].name = name }
            ok = true
        }
        return ok
    }

    /// ⌘1–⌘9: the tab at that position (from 1); past the last tab,
    /// nothing.
    public func select(number n: Int) async {
        guard n >= 1, n <= tabs.count else { return }
        await select(tabs[n - 1].id)
    }

    /// ⌘⇧]: the next tab, wrapping around.
    public func selectNext() async { await step(1) }

    /// ⌘⇧[: the previous tab, wrapping around.
    public func selectPrevious() async { await step(-1) }

    private func step(_ by: Int) async {
        guard !tabs.isEmpty else { return }
        let i = tabs.firstIndex { $0.active } ?? 0
        await select(tabs[(i + by + tabs.count) % tabs.count].id)
    }

    /// ⌘W, the close button and Close Tab: closes a tab (the current one by
    /// default). The last tab closes the window instead. A tab that still
    /// runs a program asks first; which program comes from the box agent
    /// now, not from the last update.
    public func requestClose(id: String? = nil) async -> CloseResult {
        guard let id = id ?? current?.id else { return .nothing }
        guard canClose else { return .closeWindow }
        let fresh = (try? await daemon.tabs(box)) ?? tabs
        let program = fresh.first { $0.id == id }?.program ?? ""
        if !program.isEmpty {
            pendingClose = PendingClose(id: id, program: program)
            return .asking
        }
        await close(id)
        return .closed
    }

    /// The person chose Close Tab in the question. The question's own close
    /// is passed in: SwiftUI dismisses the alert (clearing `pendingClose`)
    /// before the button's task runs, so it can't be read back here.
    public func confirmClose(_ p: PendingClose) async {
        pendingClose = nil
        await close(p.id)
    }

    /// The person chose Cancel.
    public func cancelClose() { pendingClose = nil }

    /// Shows a tab in the terminal.
    public func select(_ id: String) async {
        guard tabs.contains(where: { $0.id == id && !$0.active }) else { return }
        let before = tabs
        show(id)
        await perform {
            do {
                try await self.daemon.selectTab(self.box, id: id)
            } catch {
                self.tabs = before
                throw error
            }
        }
    }

    public func dismissError() { error = nil }

    private func show(_ id: String) {
        for i in tabs.indices { tabs[i].active = tabs[i].id == id }
    }

    private func perform(_ action: @MainActor () async throws -> Void) async {
        do {
            try await action()
        } catch let e as DaemonError {
            error = e.message
        } catch let e {
            error = e.localizedDescription
        }
    }
}
