// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Observation

/// A pane of split view (PLAN 1.2b, the split-view board): numbered 1 to 4.
/// Each pane streams its own tab (ADR 0020), so two panes never fight over a
/// current tab and an agent typing in a tab never moves a pane.
public struct PaneID: Hashable, Sendable, Comparable, CustomStringConvertible {
    public let number: Int
    public init(_ number: Int) { self.number = number }

    public static func < (a: PaneID, b: PaneID) -> Bool { a.number < b.number }
    public var description: String { "pane \(number)" }
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
    /// A tab bar tab's VoiceOver value when a pane shows it.
    public static let inAPane = "in a pane"

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

/// What ⌘W did.
public enum CommandWResult: Equatable, Sendable {
    case closedPane
    /// One pane is left: ⌘W closes the tab, as before split view.
    case closeTab
}

/// Split view: the layout, and the actions on it (Split Right ⌘D, Split
/// Down ⇧⌘D, ⌘⌥ and the arrows, ⌘W, choosing a tab), over the window's
/// TabsController. Each pane draws its own tab's terminal (ADR 0020's
/// TabTerminalSource, through MainTabTerminal); this only decides which tab
/// each pane shows.
///
/// The tab bar's current tab is always the focused pane's: choosing a tab
/// anywhere (the tab bar, ⌘1–9, ⌃Tab, ⌘T) shows it in the focused pane, or
/// focuses the pane already showing it; moving the focus makes the focused
/// pane's tab current.
@MainActor
@Observable
public final class SplitController {
    public private(set) var layout = SplitLayout(tab: nil)
    /// The panes' area, as the window last drew it.
    public var size: CGSize = .zero
    /// What the Team sidebar selects (the tab bar's, from TabsController):
    /// a new pane takes the next tab from the tabs the tab bar shows.
    public var selection: TeamSelection { tabs.selection }
    public let tabs: TabsController
    /// A tab made current by a focus change, until portenvd has it: the
    /// tab list's current tab is not followed meanwhile.
    @ObservationIgnored private var choosing: String?
    @ObservationIgnored private var pending: Task<Void, Never>?

    public init(tabs: TabsController) {
        self.tabs = tabs
    }

    /// The tab the focused pane shows (the tab bar's current tab).
    public var activeTab: String? { layout.tab(in: layout.focused) }

    public var isSplit: Bool { layout.panes.count > 1 }

    /// Whether the focused pane shows the tab the tab bar marks current
    /// (R-0314); true after every change the window passes on.
    public var agreesWithBar: Bool { activeTab != nil && activeTab == tabs.terminalTab?.id }

    /// Where a tab's pane is (0 to 1), for the tab bar's glyph; nil when no
    /// pane shows it or there's only one pane.
    public func paneFrame(forTab id: String) -> CGRect? {
        guard isSplit, let p = layout.pane(showing: id) else { return nil }
        return layout.unitFrame(of: p)
    }

    /// The tab a pane shows.
    public func tab(in pane: PaneID) -> TabInfo? {
        guard let id = layout.tab(in: pane) else { return nil }
        return tabs.tabs.first { $0.id == id }
    }

    /// A pane header's tab picker: every tab no other pane shows.
    public func pickerTabs(for pane: PaneID) -> [TabInfo] {
        tabs.tabs.filter { t in layout.pane(showing: t.id).map { $0 == pane } ?? true }
    }

    /// Follows the window's tabs (call on every change): the current tab
    /// shows in the focused pane, or focuses the pane showing it; a pane
    /// whose tab closed shows the next free tab, or closes.
    public func tabsChanged() {
        if choosing == nil, let current = tabs.current?.id, current != activeTab {
            if let other = layout.pane(showing: current) {
                layout.focus(other)
            } else {
                layout.show(current, in: layout.focused)
            }
        }
        layout.tabsChanged(tabs.tabs.map(\.id))
        followFocus()
    }

    public func canSplit(_ axis: SplitAxis) -> Bool { layout.canSplit(axis, in: size) }

    /// Splits the focused pane: the new pane shows the first tab the tab bar
    /// shows that no pane shows, or a new shell tab when every one is in a
    /// pane, and takes the focus.
    public func split(_ axis: SplitAxis) async {
        guard canSplit(axis) else { return }
        var id = layout.tabForNewPane(from: tabs.shown(for: selection).map(\.id))
        if id == nil {
            guard let made = await tabs.newTab() else { return }
            id = made.id
        }
        guard let id, layout.split(axis, showing: id, in: size) != nil else { return }
        followFocus()
    }

    /// The pane header's picker: shows a tab in that pane and focuses it.
    /// A tab another pane shows focuses that pane instead.
    public func show(_ id: String, in pane: PaneID) {
        if let other = layout.pane(showing: id) {
            layout.focus(other)
        } else {
            layout.show(id, in: pane)
            layout.focus(pane)
        }
        followFocus()
    }

    public func focus(_ pane: PaneID) {
        layout.focus(pane)
        followFocus()
    }

    /// ⌘⌥ and an arrow.
    @discardableResult
    public func moveFocus(_ d: PaneDirection) -> Bool {
        let moved = layout.moveFocus(d, in: size)
        followFocus()
        return moved
    }

    public func resize(at path: [Int], to fraction: Double) {
        layout.resize(at: path, to: fraction, in: size)
    }

    /// Closes a pane (the focused one by default). Its tab keeps running:
    /// only the pane's view of it ends.
    @discardableResult
    public func closePane(_ pane: PaneID? = nil) -> Bool {
        guard layout.close(pane ?? layout.focused) else { return false }
        followFocus()
        return true
    }

    /// ⌘W: the focused pane first; the tab only when one pane is left.
    public func commandW() -> CommandWResult { closePane() ? .closedPane : .closeTab }

    /// Waits until the tab made current by the last focus change is
    /// portenvd's too (tests).
    public func settled() async {
        while let p = pending {
            await p.value
            if pending == p { pending = nil }
        }
    }

    /// Makes the focused pane's tab the tab bar's current tab.
    private func followFocus() {
        guard let id = activeTab, id != tabs.current?.id, let task = tabs.choose(id) else { return }
        choosing = id
        pending = Task {
            await task.value
            guard choosing == id else { return }
            choosing = nil
            tabsChanged()
        }
    }
}

/// Scripted terminals for split view's renders and tests: each source starts with a reset seeded with the tab's
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

    public init(_ tab: TabInfo, now: Date, timeZone: TimeZone = .current, locale: Locale = AppClock.locale()) {
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
            who = "quiet since \(AppClock.short(last, timeZone: timeZone, locale: locale))"
        } else {
            who = ""
        }
    }

    /// What VoiceOver reads for the pane.
    public func label(tab: String) -> String {
        SplitText.paneLabel(tab: tab, status: [detail, who].filter { !$0.isEmpty }.joined(separator: ", "))
    }
}

/// Times in the app (R-0172): the app's language (the first of the bundle's
/// preferred localizations) and the Mac's own 12/24-hour setting, in the
/// short time style.
public enum AppClock {
    /// The locale times are written in.
    /// Only the language and the hour setting: the Mac's region would mix
    /// in its own words ("1:58 in the afternoon" for English in France).
    public static func locale(language: String = Bundle.main.preferredLocalizations.first ?? "en",
                              hourCycle: Locale.HourCycle = Locale.current.hourCycle) -> Locale {
        var c = Locale.Components(identifier: language)
        c.hourCycle = hourCycle
        return Locale(components: c)
    }

    /// "1:58 PM" or "13:58".
    public static func short(_ date: Date, timeZone: TimeZone = .current, locale: Locale = locale()) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = timeZone
        f.dateStyle = .none
        f.timeStyle = .short
        return f.string(from: date)
    }
}
