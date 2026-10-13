// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Observation

/// A pane of split view (PLAN 1.2b, the split-view board): numbered 1 to 4,
/// each its own viewer of the box's tabs, so two panes never fight over the
/// current tab and an agent choosing a tab never moves a pane.
public struct PaneID: Hashable, Sendable, Comparable, CustomStringConvertible {
    public let number: Int
    public init(_ number: Int) { self.number = number }

    /// The viewer the pane looks through: pane 1 is the app's existing
    /// viewer, the others `app-2` to `app-4`.
    public var viewer: String { number == 1 ? TabViewer.app : "\(TabViewer.app)-\(number)" }

    public static func < (a: PaneID, b: PaneID) -> Bool { a.number < b.number }
    public var description: String { viewer }
}

/// Split Right (side by side) or Split Down (one above the other).
public enum SplitAxis: Sendable { case right, down }

/// ⌘⌥ and an arrow.
public enum PaneDirection: Sendable { case left, right, up, down }

/// The panes' arrangement: a pane, or two arrangements split along an axis,
/// the first taking `fraction` of the space.
public indirect enum PaneNode: Equatable, Sendable {
    case pane(PaneID)
    case split(SplitAxis, PaneNode, PaneNode, fraction: Double)
}

/// The words of split view (GUIDELINES §10).
public enum SplitText {
    public static let splitRight = "Split Right"
    public static let splitDown = "Split Down"
    public static let closePane = "Close Pane"
    public static let focusLeft = "Focus Pane Left"
    public static let focusRight = "Focus Pane Right"
    public static let focusUp = "Focus Pane Above"
    public static let focusDown = "Focus Pane Below"

    /// VoiceOver's name for a pane: "Pane: grok tab, Grok Bot is typing".
    public static func paneLabel(tab: String, status: String?) -> String {
        guard let status, !status.isEmpty else { return "Pane: \(tab) tab" }
        return "Pane: \(tab) tab, \(status)"
    }

    /// The pane header's tab picker.
    public static func pickerLabel(tab: String) -> String { "Tab shown in this pane: \(tab)" }
    public static func closeLabel(tab: String) -> String { "Close the pane showing \(tab). The tab keeps running." }
}

/// Split view's layout: up to four panes, which tab each shows, and which
/// has the focus. A value: SplitController changes it and the view draws it.
public struct SplitLayout: Equatable, Sendable {
    public static let maxPanes = 4
    /// The smallest pane: the terminal keeps 340 pt (GUIDELINES §3), and a
    /// pane is never shorter than its header and a few lines.
    public static let minPane = CGSize(width: 340, height: 120)

    public private(set) var root: PaneNode
    public private(set) var focused: PaneID
    private var shown: [PaneID: String]

    public init(tab: String?) {
        root = .pane(PaneID(1))
        focused = PaneID(1)
        shown = tab.map { [PaneID(1): $0] } ?? [:]
    }

    /// The panes in reading order (left to right, then top to bottom).
    public var panes: [PaneID] { Self.leaves(root) }

    public func tab(in pane: PaneID) -> String? { shown[pane] }

    public func pane(showing tab: String) -> PaneID? { shown.first { $0.value == tab }?.key }

    /// The first of these tabs that no pane shows.
    public func tabForNewPane(from tabs: [String]) -> String? {
        tabs.first { pane(showing: $0) == nil }
    }

    public mutating func focus(_ pane: PaneID) {
        if panes.contains(pane) { focused = pane }
    }

    public mutating func show(_ tab: String, in pane: PaneID) {
        if panes.contains(pane) { shown[pane] = tab }
    }

    // MARK: Splitting and closing

    /// Whether the focused pane can split: fewer than four panes, and both
    /// halves at least the minimum.
    public func canSplit(_ axis: SplitAxis, in size: CGSize) -> Bool {
        guard panes.count < Self.maxPanes, let f = frames(in: size)[focused] else { return false }
        switch axis {
        case .right: return f.width / 2 >= Self.minPane.width
        case .down: return f.height / 2 >= Self.minPane.height
        }
    }

    /// Splits the focused pane in two; the new pane (the lowest free
    /// number) shows `tab` and gets the focus. Nil when it can't split.
    @discardableResult
    public mutating func split(_ axis: SplitAxis, showing tab: String, in size: CGSize) -> PaneID? {
        guard canSplit(axis, in: size) else { return nil }
        let used = Set(panes)
        guard let new = (1 ... Self.maxPanes).map(PaneID.init).first(where: { !used.contains($0) }) else { return nil }
        root = Self.replacing(focused, in: root, with: .split(axis, .pane(focused), .pane(new), fraction: 0.5))
        shown[new] = tab
        focused = new
        return new
    }

    /// Closes a pane: its neighbour takes the space and, if it had the
    /// focus, the focus. The last pane never closes. Its tab keeps running.
    @discardableResult
    public mutating func close(_ pane: PaneID) -> Bool {
        guard panes.count > 1, panes.contains(pane), let (rest, heir) = Self.removing(pane, from: root) else { return false }
        root = rest
        shown[pane] = nil
        if focused == pane { focused = heir }
        return true
    }

    /// Follows the box's tabs: a pane whose tab has gone shows the first
    /// tab no pane shows, or closes if every tab is shown. Returns the
    /// panes that now show another tab.
    @discardableResult
    public mutating func tabsChanged(_ tabs: [String]) -> [PaneID: String] {
        var changed: [PaneID: String] = [:]
        for pane in panes where !tabs.contains(shown[pane] ?? "") {
            shown[pane] = nil
            if let next = tabForNewPane(from: tabs) {
                shown[pane] = next
                changed[pane] = next
            } else if panes.count > 1 {
                close(pane)
            }
        }
        return changed
    }

    // MARK: Geometry

    /// Each pane's frame in a space of this size (y from the top).
    public func frames(in size: CGSize) -> [PaneID: CGRect] {
        var out: [PaneID: CGRect] = [:]
        Self.layout(root, in: CGRect(origin: .zero, size: size), points: true, into: &out)
        return out
    }

    /// Where a pane is in the window, from 0 to 1 (the tab bar's glyph).
    public func unitFrame(of pane: PaneID) -> CGRect? {
        var out: [PaneID: CGRect] = [:]
        Self.layout(root, in: CGRect(x: 0, y: 0, width: 1, height: 1), points: false, into: &out)
        return out[pane]
    }

    /// ⌘⌥ and an arrow: the nearest pane on that side that lines up with
    /// the focused one. False when there's none.
    @discardableResult
    public mutating func moveFocus(_ direction: PaneDirection, in size: CGSize) -> Bool {
        let all = frames(in: size)
        guard let here = all[focused] else { return false }
        let eps: CGFloat = 0.5
        let candidates = all.filter { id, f in
            guard id != focused else { return false }
            switch direction {
            case .left: return f.maxX <= here.minX + eps && Self.overlap(f.minY, f.maxY, here.minY, here.maxY) > 0
            case .right: return f.minX >= here.maxX - eps && Self.overlap(f.minY, f.maxY, here.minY, here.maxY) > 0
            case .up: return f.maxY <= here.minY + eps && Self.overlap(f.minX, f.maxX, here.minX, here.maxX) > 0
            case .down: return f.minY >= here.maxY - eps && Self.overlap(f.minX, f.maxX, here.minX, here.maxX) > 0
            }
        }
        // Nearest first; then the one most in line; then top, then left.
        let best = candidates.min { a, b in
            let da = Self.gap(a.value, here, direction), db = Self.gap(b.value, here, direction)
            if da != db { return da < db }
            let oa = Self.lineUp(a.value, here, direction), ob = Self.lineUp(b.value, here, direction)
            if oa != ob { return oa > ob }
            return (a.value.minY, a.value.minX) < (b.value.minY, b.value.minX)
        }
        guard let best else { return false }
        focused = best.key
        return true
    }

    /// Drags the divider of the split at `path` (0 for the first child, 1
    /// for the second, from the root) to `fraction`, keeping both sides at
    /// least their minimum.
    public mutating func resize(at path: [Int], to fraction: Double, in size: CGSize) {
        guard let rect = Self.rect(of: path, in: root, frame: CGRect(origin: .zero, size: size)) else { return }
        root = Self.setFraction(at: path[...], in: root) { axis, first, second in
            let total = axis == .right ? rect.width : rect.height
            guard total > 0 else { return fraction }
            let lo = Self.minLength(first, axis) / total
            let hi = 1 - Self.minLength(second, axis) / total
            return lo > hi ? 0.5 : min(max(fraction, lo), hi)
        }
    }

    /// A divider between two panes or groups: the split's path (for
    /// resize), its axis, the line it draws, and the space it divides.
    public struct Divider: Equatable, Sendable {
        public let path: [Int]
        public let axis: SplitAxis
        public let line: CGRect
        public let span: CGRect
    }

    /// Every divider, outermost first.
    public func dividers(in size: CGSize) -> [Divider] {
        var out: [Divider] = []
        Self.dividers(root, path: [], in: CGRect(origin: .zero, size: size), into: &out)
        return out
    }

    private static func dividers(_ n: PaneNode, path: [Int], in r: CGRect, into out: inout [Divider]) {
        guard case let .split(axis, a, b, f) = n else { return }
        let (ra, rb) = divide(r, axis, f)
        let line = axis == .right ? CGRect(x: rb.minX, y: r.minY, width: 0, height: r.height)
            : CGRect(x: r.minX, y: rb.minY, width: r.width, height: 0)
        out.append(Divider(path: path, axis: axis, line: line, span: r))
        dividers(a, path: path + [0], in: ra, into: &out)
        dividers(b, path: path + [1], in: rb, into: &out)
    }

    // MARK: Tree helpers

    private static func leaves(_ n: PaneNode) -> [PaneID] {
        switch n {
        case let .pane(p): [p]
        case let .split(_, a, b, _): leaves(a) + leaves(b)
        }
    }

    private static func replacing(_ p: PaneID, in n: PaneNode, with new: PaneNode) -> PaneNode {
        switch n {
        case .pane(p): new
        case .pane: n
        case let .split(axis, a, b, f): .split(axis, replacing(p, in: a, with: new), replacing(p, in: b, with: new), fraction: f)
        }
    }

    /// The tree without the pane, and the pane that takes its place (the
    /// first pane of its sibling).
    private static func removing(_ p: PaneID, from n: PaneNode) -> (PaneNode, PaneID)? {
        guard case let .split(axis, a, b, f) = n else { return nil }
        if a == .pane(p) { return (b, leaves(b)[0]) }
        if b == .pane(p) { return (a, leaves(a)[0]) }
        if let (ra, heir) = removing(p, from: a) { return (.split(axis, ra, b, fraction: f), heir) }
        if let (rb, heir) = removing(p, from: b) { return (.split(axis, a, rb, fraction: f), heir) }
        return nil
    }

    private static func layout(_ n: PaneNode, in r: CGRect, points: Bool, into out: inout [PaneID: CGRect]) {
        switch n {
        case let .pane(p): out[p] = r
        case let .split(axis, a, b, f):
            let (ra, rb) = divide(r, axis, f, points: points)
            layout(a, in: ra, points: points, into: &out)
            layout(b, in: rb, points: points, into: &out)
        }
    }

    /// Splits a frame; in points, at a whole point.
    private static func divide(_ r: CGRect, _ axis: SplitAxis, _ f: Double, points: Bool = true) -> (CGRect, CGRect) {
        let whole: (CGFloat) -> CGFloat = { points ? $0.rounded() : $0 }
        switch axis {
        case .right:
            let w = whole(r.width * f)
            return (CGRect(x: r.minX, y: r.minY, width: w, height: r.height),
                    CGRect(x: r.minX + w, y: r.minY, width: r.width - w, height: r.height))
        case .down:
            let h = whole(r.height * f)
            return (CGRect(x: r.minX, y: r.minY, width: r.width, height: h),
                    CGRect(x: r.minX, y: r.minY + h, width: r.width, height: r.height - h))
        }
    }

    /// The frame of the node at `path`.
    private static func rect(of path: [Int], in n: PaneNode, frame: CGRect) -> CGRect? {
        guard let first = path.first else { return frame }
        guard case let .split(axis, a, b, f) = n else { return nil }
        let (ra, rb) = divide(frame, axis, f)
        return first == 0 ? rect(of: Array(path.dropFirst()), in: a, frame: ra) : rect(of: Array(path.dropFirst()), in: b, frame: rb)
    }

    private static func setFraction(at path: ArraySlice<Int>, in n: PaneNode,
                                     _ clamp: (SplitAxis, PaneNode, PaneNode) -> Double) -> PaneNode {
        guard case let .split(axis, a, b, f) = n else { return n }
        guard let first = path.first else { return .split(axis, a, b, fraction: clamp(axis, a, b)) }
        let rest = path.dropFirst()
        return first == 0 ? .split(axis, setFraction(at: rest, in: a, clamp), b, fraction: f)
            : .split(axis, a, setFraction(at: rest, in: b, clamp), fraction: f)
    }

    /// The least length a subtree needs along an axis.
    private static func minLength(_ n: PaneNode, _ along: SplitAxis) -> CGFloat {
        switch n {
        case .pane: return along == .right ? minPane.width : minPane.height
        case let .split(axis, a, b, _):
            return axis == along ? minLength(a, along) + minLength(b, along) : max(minLength(a, along), minLength(b, along))
        }
    }

    private static func overlap(_ a0: CGFloat, _ a1: CGFloat, _ b0: CGFloat, _ b1: CGFloat) -> CGFloat {
        max(0, min(a1, b1) - max(a0, b0))
    }

    private static func gap(_ f: CGRect, _ here: CGRect, _ d: PaneDirection) -> CGFloat {
        switch d {
        case .left: here.minX - f.maxX
        case .right: f.minX - here.maxX
        case .up: here.minY - f.maxY
        case .down: f.minY - here.maxY
        }
    }

    private static func lineUp(_ f: CGRect, _ here: CGRect, _ d: PaneDirection) -> CGFloat {
        switch d {
        case .left, .right: overlap(f.minY, f.maxY, here.minY, here.maxY)
        case .up, .down: overlap(f.minX, f.maxX, here.minX, here.maxX)
        }
    }
}

/// What a pane's terminal can do, as split view sees it (R-0131). Today
/// each pane attaches as its own viewer of the box's grouped tmux session;
/// lane A's per-tab terminal can replace that behind this protocol without
/// touching the layout, focus or commands.
@MainActor
public protocol PaneTerminals: AnyObject {
    /// Shows a tab in a pane.
    func show(tab: String, in pane: PaneID) async throws
    /// Makes a new tab, shown in that pane only.
    func newTab(name: String, in pane: PaneID) async throws -> TabInfo
    /// The pane closed: its terminal ends; the tab keeps running.
    func closed(_ pane: PaneID)
}

/// What ⌘W did.
public enum CommandWResult: Equatable, Sendable {
    case closedPane
    /// One pane is left: ⌘W closes the tab, as before split view.
    case closeTab
}

/// Split view: the layout, and the actions on it (Split Right ⌘D, Split
/// Down ⇧⌘D, ⌘⌥ and the arrows, ⌘W, choosing a tab).
@MainActor
@Observable
public final class SplitController {
    public private(set) var layout = SplitLayout(tab: nil)
    public private(set) var tabs: [TabInfo] = []
    /// The last failure, said once.
    public private(set) var error: String?
    private let terminals: PaneTerminals
    private var pending: Task<Void, Never>?

    public init(terminals: PaneTerminals) {
        self.terminals = terminals
    }

    /// The tab the focused pane shows (the tab bar's current tab).
    public var activeTab: String? { layout.tab(in: layout.focused) }

    public var isSplit: Bool { layout.panes.count > 1 }

    /// Where a tab's pane is (0 to 1), for the tab bar's glyph; nil when
    /// no pane shows it or there's only one pane.
    public func paneFrame(forTab id: String) -> CGRect? {
        guard isSplit, let p = layout.pane(showing: id) else { return nil }
        return layout.unitFrame(of: p)
    }

    public func tab(_ id: String?) -> TabInfo? { tabs.first { $0.id == id } }

    /// The box's tabs, as the app's viewer (pane 1) sees them.
    public func tabsChanged(_ new: [TabInfo]) {
        tabs = new
        if layout.tab(in: PaneID(1)) == nil || !isSplit, let active = new.first(where: \.active) {
            // Pane 1 is the app's viewer: it shows that viewer's tab.
            if layout.panes.contains(PaneID(1)) { layout.show(active.id, in: PaneID(1)) }
        }
        let changed = layout.tabsChanged(new.map(\.id))
        guard !changed.isEmpty else { return }
        let before = pending
        pending = Task {
            await before?.value
            for (pane, tab) in changed.sorted(by: { $0.key < $1.key }) {
                await perform { try await self.terminals.show(tab: tab, in: pane) }
            }
        }
    }

    /// Waits for shows started by a change of tabs (tests).
    public func settled() async { await pending?.value }

    public func canSplit(_ axis: SplitAxis, in size: CGSize) -> Bool { layout.canSplit(axis, in: size) }

    /// Splits the focused pane: the new pane shows the first tab no pane
    /// shows, or a new tab of its own when every tab is shown.
    public func split(_ axis: SplitAxis, in size: CGSize) async {
        guard layout.canSplit(axis, in: size) else { return }
        var trial = layout
        if let tab = layout.tabForNewPane(from: tabs.map(\.id)) {
            guard let pane = trial.split(axis, showing: tab, in: size) else { return }
            await perform {
                try await self.terminals.show(tab: tab, in: pane)
                self.layout = trial
            }
        } else {
            let name = TabNames.next(after: tabs.map(\.name))
            let pane = trial.split(axis, showing: "", in: size)
            guard let pane else { return }
            await perform {
                let t = try await self.terminals.newTab(name: name, in: pane)
                trial.show(t.id, in: pane)
                if !self.tabs.contains(where: { $0.id == t.id }) { self.tabs.append(t) }
                self.layout = trial
            }
        }
    }

    /// Shows a tab in the focused pane; a tab another pane shows moves the
    /// focus there instead.
    public func show(_ id: String) async {
        if let other = layout.pane(showing: id) {
            layout.focus(other)
            return
        }
        let pane = layout.focused
        await perform {
            try await self.terminals.show(tab: id, in: pane)
            self.layout.show(id, in: pane)
        }
    }

    public func showNumber(_ n: Int) async {
        guard n >= 1, n <= tabs.count else { return }
        await show(tabs[n - 1].id)
    }

    public func showNext() async { await step(1) }
    public func showPrevious() async { await step(-1) }

    private func step(_ by: Int) async {
        guard !tabs.isEmpty else { return }
        let i = tabs.firstIndex { $0.id == activeTab } ?? 0
        await show(tabs[(i + by + tabs.count) % tabs.count].id)
    }

    public func focus(_ pane: PaneID) { layout.focus(pane) }

    @discardableResult
    public func moveFocus(_ d: PaneDirection, in size: CGSize) -> Bool { layout.moveFocus(d, in: size) }

    public func resize(at path: [Int], to fraction: Double, in size: CGSize) {
        layout.resize(at: path, to: fraction, in: size)
    }

    /// Closes a pane (the focused one by default); never its tab.
    @discardableResult
    public func closePane(_ pane: PaneID? = nil) -> Bool {
        let p = pane ?? layout.focused
        guard layout.close(p) else { return false }
        terminals.closed(p)
        return true
    }

    @discardableResult
    public func closeFocusedPane() -> Bool { closePane() }

    /// ⌘W: the focused pane first; the tab only when one pane is left.
    public func commandW() -> CommandWResult { closePane() ? .closedPane : .closeTab }

    public func dismissError() { error = nil }

    private func perform(_ action: @MainActor () async throws -> Void) async {
        do {
            try await action()
        } catch let e as DaemonError {
            error = e.message
        } catch {
            self.error = error.localizedDescription
        }
    }
}

/// Split view's terminals over ADR 0020's interface: each pane opens its own
/// source from the workspace's TabTerminalProvider (ScriptedTabSource until
/// 1.2c's provider lands, R-0171). Choosing another tab opens a new source
/// and closes the old one; closing a pane closes its source, never the tab.
@MainActor
@Observable
public final class PaneSources: PaneTerminals {
    /// The size a source opens with; the view sends its real size at once.
    public static let startSize = TabTerminalSize(cols: 80, rows: 24)

    private let provider: TabTerminalProvider
    private let makeTab: (String, PaneID) async throws -> TabInfo
    private var sources: [PaneID: any TabTerminalSource] = [:]

    /// - newTab: makes a tab for one pane's viewer (portenvd's NewTab).
    public init(provider: TabTerminalProvider, newTab: @escaping (String, PaneID) async throws -> TabInfo) {
        self.provider = provider
        makeTab = newTab
    }

    public func source(for pane: PaneID) -> (any TabTerminalSource)? { sources[pane] }

    public func show(tab: String, in pane: PaneID) async throws {
        if sources[pane]?.tab == tab { return }
        sources[pane]?.close()
        sources[pane] = provider.open(tab: tab, size: Self.startSize)
    }

    public func newTab(name: String, in pane: PaneID) async throws -> TabInfo {
        let t = try await makeTab(name, pane)
        try await show(tab: t.id, in: pane)
        return t
    }

    public func closed(_ pane: PaneID) {
        sources.removeValue(forKey: pane)?.close()
    }
}

/// Scripted terminals for split view until 1.2c's provider lands (R-0171),
/// and for renders: each source starts with a reset seeded with the tab's
/// sample text.
@MainActor
public final class ScriptedTabProvider: TabTerminalProvider {
    /// Each tab's sample screen, by tab ID.
    public var seeds: [String: String]
    public init(seeds: [String: String] = [:]) { self.seeds = seeds }

    public func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource {
        let s = ScriptedTabSource(tab: tab)
        let text = (seeds[tab] ?? "").replacingOccurrences(of: "\n", with: "\r\n")
        s.play(.reset(size, seed: Array(text.utf8)))
        return s
    }
}

/// A pane header's words (the split-view board): on the left, the program
/// and what it's doing ("Claude Code · asking you", "watching"); on the
/// right, who is typing ("You", "Grok Bot is typing") or "quiet since 15:41".
public struct PaneHeader: Equatable, Sendable {
    public let detail: String
    public let who: String

    public init(detail: String, who: String) {
        self.detail = detail
        self.who = who
    }

    public init(_ tab: TabInfo, now: Date, timeZone: TimeZone = .current) {
        let program = tab.program
        if tab.waiting != nil {
            detail = program.isEmpty ? "asking you" : "\(program) \u{00B7} asking you"
        } else if !tab.watchers.isEmpty {
            detail = "watching"
        } else {
            detail = program
        }
        if let author = tab.typingAuthor {
            who = author.isYou ? "You" : "\(author.name) is typing"
        } else if !program.isEmpty, let last = tab.lastOutputAt, now.timeIntervalSince(last) >= TabActivity.quietAfter {
            let f = DateFormatter()
            f.locale = Locale(identifier: "en_GB")
            f.timeZone = timeZone
            f.dateFormat = "HH:mm"
            who = "quiet since \(f.string(from: last))"
        } else {
            who = ""
        }
    }

    /// What VoiceOver reads for the pane.
    public func label(tab: String) -> String {
        SplitText.paneLabel(tab: tab, status: [detail, who].filter { !$0.isEmpty }.joined(separator: ", "))
    }
}
