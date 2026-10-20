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
    /// What the tab's program is asking, while it waits for an answer
    /// (ADR 0017's detector; from 1.2c's tab events). For "asking you", the
    /// answer card and the badge.
    public var waiting: TabQuestion?
    /// When the tab last printed anything.
    public var lastOutputAt: Date?
    /// Who is typing in the tab now (the author of the latest input).
    public var typingAuthor: TabAuthor?
    /// The other viewers that have the tab open without typing (agents, a
    /// CLI attach): the pane header's "watching".
    public var watchers: [String]
    /// The foreground command's whole command line, one line, for the Move
    /// confirmation (2.3); empty when it's just the shell. It can hold a
    /// secret (mysql -p…): shown on this Mac only, and never printed: a
    /// tab's description and mirror leave it out (R-0140).
    public var command: String
    /// When that command started.
    public var runningSince: Date?
    /// The agent whose tmux server the tab is on (ADR 0022 §1); empty for
    /// the owner's tabs.
    public var agentID: String
    /// The tab's panes (tmux pane IDs), in tmux's order.
    public var paneIDs: [String]

    public init(id: String, name: String, active: Bool, program: String = "", waiting: TabQuestion? = nil,
                lastOutputAt: Date? = nil, typingAuthor: TabAuthor? = nil, watchers: [String] = [],
                command: String = "", runningSince: Date? = nil, agentID: String = "", paneIDs: [String] = []) {
        self.agentID = agentID
        self.paneIDs = paneIDs
        self.command = command
        self.runningSince = runningSince
        self.id = id
        self.name = name
        self.active = active
        self.program = program
        self.waiting = waiting
        self.lastOutputAt = lastOutputAt
        self.typingAuthor = typingAuthor
        self.watchers = watchers
    }

    /// The pane header's short status, most important first: asking,
    /// someone typing, someone watching, quiet for a while, running, idle.
    public func activity(now: Date) -> TabActivity {
        if let waiting { return .asking(waiting) }
        if let typingAuthor { return .typing(typingAuthor) }
        if !watchers.isEmpty { return .watching(watchers) }
        if !program.isEmpty, let last = lastOutputAt {
            return now.timeIntervalSince(last) >= TabActivity.quietAfter ? .quiet(since: last) : .running(program)
        }
        return program.isEmpty ? .idle : .running(program)
    }
}

extension TabInfo: CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public var description: String { "[tab \(id) \(name)\(program.isEmpty ? "" : ", running \(program)")]" }
    public var debugDescription: String { description }
    public var customMirror: Mirror {
        Mirror(self, children: ["id": id, "name": name, "active": active, "program": program], displayStyle: .struct)
    }
}

/// A question a tab's program is waiting on. A secret prompt (a password)
/// carries no text: it's answered only in the terminal (ADR 0016, 0017).
public struct TabQuestion: Equatable, Sendable {
    public let text: String
    public let secret: Bool

    public init(text: String, secret: Bool) {
        self.text = secret ? "" : text
        self.secret = secret
    }
}

/// Who typed: a name, and whether it's the person at this Mac.
public struct TabAuthor: Equatable, Sendable {
    public let name: String
    public let isYou: Bool

    public init(name: String, isYou: Bool) {
        self.name = name
        self.isYou = isYou
    }
}

/// A tab's status for the pane header (split view): "asking you", "Grok Bot
/// is typing", "watching", "quiet since 15:41".
public enum TabActivity: Equatable, Sendable {
    case asking(TabQuestion)
    case typing(TabAuthor)
    case watching([String])
    case quiet(since: Date)
    case running(String)
    case idle

    /// A running program that has printed nothing for this long is quiet.
    public static let quietAfter: TimeInterval = 60
}

/// The app's viewer name: the box agent gives this app its own grouped tmux
/// session, so an agent choosing a tab never moves the app's (1.2).
public enum TabViewer {
    public static let app = "app"
}

/// ⌃Tab and ⌃⇧Tab (GUIDELINES §3.3): the next and previous workspace tab,
/// as macOS's Window › Show Next Tab and Show Previous Tab (R-0345). Next
/// is 1, previous is -1; any other key, or Tab with ⌥ or ⌘, is not a tab
/// key and returns nil.
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
    /// Window menu: the terminal's tabs, named apart from macOS's own Show
    /// Previous Tab and Show Next Tab, which go between workspace tabs
    /// (R-0345).
    public static let showPrevious = "Show Previous Terminal Tab"
    public static let showNext = "Show Next Terminal Tab"
    public static let showTab = "Show Terminal Tab"
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

    /// The tab portenvd shows this app (at most one is active). The
    /// terminal never reads this directly: it shows `terminalTab`.
    public var current: TabInfo? { tabs.first { $0.active } }

    /// What the Team sidebar selects (R-0241, R-0314): the tab bar and the
    /// terminal both read it from here, so they can't disagree. Set with
    /// `view(_:)`.
    public private(set) var selection: TeamSelection = .allTabs

    /// Told when this controller widens the selection itself, to .allTabs:
    /// a tab outside an agent's filter became current (a new tab, a click
    /// in a split pane showing another agent's tab), so the sidebar
    /// follows.
    @ObservationIgnored public var selectionWidened: (@MainActor (TeamSelection) -> Void)?

    /// The tab each agent and the owner last had current, to go back to.
    @ObservationIgnored private var lastOwnTab: String?
    @ObservationIgnored private var lastAgentTab: [String: String] = [:]

    /// The tabs the tab bar shows for the selection.
    public var shownTabs: [TabInfo] { shown(for: selection) }

    /// The one tab the terminal (the focused pane, when split) shows and the
    /// tab bar marks current (R-0314): the current tab, only while it is in
    /// the bar. Nil while it isn't (the selected agent has no tab open): the
    /// terminal shows an empty state then, never another agent's pane.
    public var terminalTab: TabInfo? {
        guard let c = current, shownTabs.contains(where: { $0.id == c.id }) else { return nil }
        return c
    }

    /// The owner chose something in the Team sidebar (or All tabs): the
    /// tab bar filters to it, and the terminal switches with it at once.
    /// An agent: its tab last shown, or its first. All tabs or You: the
    /// owner's tab last shown, or their first. Nothing changes for the same
    /// selection, or when what's current already belongs to it.
    @discardableResult
    public func view(_ s: TeamSelection) -> Task<Void, Never>? {
        guard s != selection else { return nil }
        selection = s
        remember()
        guard let id = target(for: s) else { return nil }
        return choose(id)
    }

    /// The tab to show for a selection, or nil to leave the terminal as it
    /// is (what's current already belongs to it, or there's nothing to
    /// show).
    private func target(for s: TeamSelection) -> String? {
        let exists = { (id: String?) in id.flatMap { id in self.tabs.contains { $0.id == id } ? id : nil } }
        switch s {
        case .agent(let agent):
            if current?.agentID == agent { return nil }
            let mine = lastAgentTab[agent].flatMap { id in self.tabs.contains { $0.id == id && $0.agentID == agent } ? id : nil }
            return mine ?? tabs.first { $0.agentID == agent }?.id
        case .allTabs, .you:
            if let c = current, c.agentID.isEmpty { return nil }
            return exists(lastOwnTab) ?? tabs.first { $0.agentID.isEmpty }?.id
        case .decision, .activity:
            return current == nil ? tabs.first?.id : nil
        }
    }

    /// Keeps the terminal on a tab the bar shows after the tabs changed
    /// underneath (an agent's tab closed, portenvd went back to the
    /// owner's): the selected agent's other tab, or the owner's. An agent
    /// starting doesn't change what's current, so it never moves the view
    /// (PLAN 4b).
    private func tabsUpdated() {
        remember()
        if terminalTab == nil, let id = target(for: selection) { choose(id) }
    }

    private func remember() {
        guard let c = current else { return }
        if c.agentID.isEmpty { lastOwnTab = c.id } else { lastAgentTab[c.agentID] = c.id }
    }

    /// The selection goes back to every tab, so the bar shows what's
    /// current.
    private func widenIfHidden(_ id: String) {
        guard !shownTabs.contains(where: { $0.id == id }) else { return }
        selection = .allTabs
        selectionWidened?(.allTabs)
    }

    /// The tabs to show for what the Team sidebar selects (R-0241): every
    /// tab, the owner's own, or one agent's.
    public func shown(for selection: TeamSelection) -> [TabInfo] {
        switch selection {
        case .allTabs, .decision, .activity: tabs
        case .you: tabs.filter { $0.agentID.isEmpty }
        case .agent(let id): tabs.filter { $0.agentID == id }
        }
    }

    /// Reads the tabs once.
    public func load() async {
        if let ts = try? await daemon.tabs(box) {
            tabs = ts
            tabsUpdated()
        }
    }

    /// Follows the tabs as the box agent reports them, watching again after
    /// the stream ends (the box restarted, moved, or portenvd went away).
    /// Runs until the task is cancelled.
    public func follow() async {
        while !Task.isCancelled {
            do {
                for try await ts in daemon.watchTabs(box) {
                    tabs = ts
                    tabsUpdated()
                    for f in commands.update(ts, at: now()) { commandFinished?(f) }
                }
            } catch {}
            if Task.isCancelled { return }
            try? await Task.sleep(for: retry)
        }
    }

    /// `+`: a new tab at the end, shown at once. The new tab, or nil when
    /// it couldn't be made (the problem is in `error`).
    @discardableResult
    public func newTab() async -> TabInfo? {
        let name = TabNames.next(after: tabs.map(\.name))
        var made: TabInfo?
        await perform {
            let t = try await self.daemon.newTab(self.box, name: name)
            if !self.tabs.contains(where: { $0.id == t.id }) { self.tabs.append(t) }
            self.show(t.id)
            self.widenIfHidden(t.id)
            made = t
        }
        return made
    }

    /// Takes the tabs as the box agent reported them, as `follow` does
    /// (renders and tests).
    public func receive(_ ts: [TabInfo]) {
        tabs = ts
        tabsUpdated()
    }

    /// Closes a tab, never the last one.
    public func close(_ id: String) async {
        guard canClose else { return }
        await perform {
            try await self.daemon.closeTab(self.box, id: id)
            let wasActive = self.tabs.first { $0.id == id }?.active ?? false
            self.tabs.removeAll { $0.id == id }
            if wasActive, let last = self.shownTabs.last ?? self.tabs.last { self.show(last.id) }
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

    /// ⌘1–⌘9: the tab at that position in the bar (from 1); past the last
    /// tab, nothing.
    public func select(number n: Int) async {
        let shown = shownTabs
        guard n >= 1, n <= shown.count else { return }
        await select(shown[n - 1].id)
    }

    /// ⌘⇧]: the next tab, wrapping around.
    public func selectNext() async { await step(1) }

    /// ⌘⇧[: the previous tab, wrapping around.
    public func selectPrevious() async { await step(-1) }

    /// Steps through the tabs the bar shows, never to one it hides.
    private func step(_ by: Int) async {
        let shown = shownTabs
        guard !shown.isEmpty else { return }
        guard let i = shown.firstIndex(where: { $0.active }) else {
            await select(shown[by > 0 ? 0 : shown.count - 1].id)
            return
        }
        await select(shown[(i + by + shown.count) % shown.count].id)
    }

    /// ⌘W, the close button and Close Tab: closes a tab (the one the
    /// terminal shows by default; none while it shows no tab). The last tab closes the window instead. A tab that still
    /// runs a program asks first; which program comes from the box agent
    /// now, not from the last update.
    public func requestClose(id: String? = nil) async -> CloseResult {
        guard let id = id ?? terminalTab?.id else { return .nothing }
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
        await choose(id)?.value
    }

    /// Marks a tab current at once, and tells portenvd after (split view's
    /// focus, which can't wait for the answer). Nil when there's nothing to
    /// do: the tab is current already, or gone. A refusal puts the tabs
    /// back as they were. A tab the bar hides (a split pane showing another
    /// agent's tab) widens the selection to every tab, so the bar marks it.
    @discardableResult
    public func choose(_ id: String) -> Task<Void, Never>? {
        guard tabs.contains(where: { $0.id == id && !$0.active }) else { return nil }
        let before = tabs
        show(id)
        widenIfHidden(id)
        return Task {
            await perform {
                do {
                    try await self.daemon.selectTab(self.box, id: id)
                } catch {
                    self.tabs = before
                    throw error
                }
            }
        }
    }

    public func dismissError() { error = nil }

    private func show(_ id: String) {
        for i in tabs.indices { tabs[i].active = tabs[i].id == id }
        remember()
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
