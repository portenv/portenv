// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// A tab of the box's terminal: one tmux window in the box (GUIDELINES.md
/// §3, §4.1). The ID is stable for the tab's life.
public struct TabInfo: Equatable, Sendable, Identifiable {
    public let id: String
    public var name: String
    /// The tab the terminal shows.
    public var active: Bool

    public init(id: String, name: String, active: Bool) {
        self.id = id
        self.name = name
        self.active = active
    }
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

    public init(box: String, daemon: DaemonAPI, retry: Duration = .seconds(1)) {
        self.box = box
        self.daemon = daemon
        self.retry = retry
    }

    /// A box keeps at least one tab.
    public var canClose: Bool { tabs.count > 1 }

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
                for try await ts in daemon.watchTabs(box) { tabs = ts }
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
