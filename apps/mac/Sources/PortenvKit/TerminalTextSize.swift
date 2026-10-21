// SPDX-License-Identifier: Apache-2.0

import AppKit
import Observation

/// The terminal's text size (R-0362, GUIDELINES.md §4.5): View › Bigger
/// (⌘+, also ⌘= and ⌘⇧=) and View › Smaller (⌘−), as in Terminal.app. Only
/// the terminal's text changes; the sidebar, team column and the rest of the
/// window keep their size. Smaller never goes below the default size (the
/// owner, R-0362), and there is no Default Size command: ⌘0 stays View ›
/// Show Sidebar.
public enum TerminalTextSize {
    /// The terminal's size, in points: SF Mono 13 pt (§4.5). It is also the
    /// smallest: Smaller stops here.
    public static let defaultPoints: CGFloat = 13
    public static let minimum: CGFloat = defaultPoints
    public static let maximum: CGFloat = 36
    /// Each Bigger or Smaller moves one point.
    public static let step: CGFloat = 1

    public enum Step: Equatable, Sendable {
        case bigger, smaller
    }

    /// A size within the range, on a whole point.
    public static func clamp(_ points: CGFloat) -> CGFloat {
        guard points.isFinite else { return defaultPoints }
        return min(maximum, max(minimum, points.rounded()))
    }

    /// The size one step on, or nil at the limit (the menu item is then
    /// disabled).
    public static func applying(_ step: Step, to points: CGFloat) -> CGFloat? {
        let now = clamp(points)
        let next = clamp(step == .bigger ? now + Self.step : now - Self.step)
        return next == now ? nil : next
    }

    // MARK: Remembered per workspace

    /// The defaults key for a workspace's size, beside its other view state
    /// (`inspectorOpen.<box>`, `teamSidebar.<box>`). Not a secret: never the
    /// Keychain.
    public static func key(_ box: String) -> String { "terminalTextSize.\(box)" }

    /// The workspace's size: the last one chosen in it, or the default.
    public static func stored(box: String, in defaults: UserDefaults = .standard) -> CGFloat {
        guard defaults.object(forKey: key(box)) != nil else { return defaultPoints }
        return clamp(CGFloat(defaults.double(forKey: key(box))))
    }

    /// Remembers the workspace's size; the default is remembered as no value.
    public static func store(_ points: CGFloat, box: String, in defaults: UserDefaults = .standard) {
        let points = clamp(points)
        if points == defaultPoints {
            defaults.removeObject(forKey: key(box))
        } else {
            defaults.set(Double(points), forKey: key(box))
        }
    }

    // MARK: Words

    /// What VoiceOver says after a change.
    public static func announcement(_ points: CGFloat) -> String {
        "Terminal text size \(Int(clamp(points))) points"
    }

    // MARK: Keys

    /// The View menu's own shortcuts: ⌘+ and ⌘− (KeyEquivalent characters).
    public static let biggerKey: Character = "+"
    public static let smallerKey: Character = "-"

    /// What a ⌘ key press means for the text size, or nil. Bigger is ⌘+,
    /// ⌘= and ⌘⇧= (so a US layout, where + needs Shift, and AZERTY, where =
    /// and + share a key, both work); Smaller is ⌘−. Option or Control with
    /// it is something else, and goes on to the program.
    public static func step(charactersIgnoringModifiers characters: String?,
                            modifiers: NSEvent.ModifierFlags) -> Step? {
        let flags = modifiers.intersection(.deviceIndependentFlagsMask)
        guard flags.contains(.command), flags.isDisjoint(with: [.option, .control]) else { return nil }
        switch characters {
        case "=", "+": return .bigger
        case "-" where !flags.contains(.shift): return .smaller
        default: return nil
        }
    }
}

/// One View menu item for the text size (R-0362).
public struct TerminalTextSizeMenuItem: Equatable, Sendable {
    public let step: TerminalTextSize.Step
    public let title: String
    public let key: Character
    /// VoiceOver's label for the item.
    public let accessibilityLabel: String
    public let isEnabled: Bool
}

public enum TerminalTextSizeMenu {
    /// View › Bigger and Smaller for the focused terminal's size, or both
    /// disabled when no terminal is focused. Nothing else: no Default Size
    /// (the owner, R-0362).
    public static func items(points: CGFloat?) -> [TerminalTextSizeMenuItem] {
        [
            TerminalTextSizeMenuItem(step: .bigger, title: "Bigger", key: TerminalTextSize.biggerKey,
                                     accessibilityLabel: "Make the terminal text bigger",
                                     isEnabled: points.map { TerminalTextSize.applying(.bigger, to: $0) != nil } ?? false),
            TerminalTextSizeMenuItem(step: .smaller, title: "Smaller", key: TerminalTextSize.smallerKey,
                                     accessibilityLabel: "Make the terminal text smaller",
                                     isEnabled: points.map { TerminalTextSize.applying(.smaller, to: $0) != nil } ?? false),
        ]
    }
}

/// One terminal's text size: it starts at its workspace's size, and a
/// change is remembered for the workspace, so its new tabs and panes, and
/// the next launch, start at it. VoiceOver hears the new size.
@MainActor
@Observable
public final class TerminalTextSizeState {
    public let box: String
    public private(set) var points: CGFloat
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let announce: @MainActor (String) -> Void

    public init(box: String, defaults: UserDefaults = .standard, announce: @escaping @MainActor (String) -> Void = { _ in }) {
        self.box = box
        self.defaults = defaults
        self.announce = announce
        points = TerminalTextSize.stored(box: box, in: defaults)
    }

    public var menu: [TerminalTextSizeMenuItem] { TerminalTextSizeMenu.items(points: points) }

    /// Takes one step; false at the limit, where nothing changes.
    @discardableResult
    public func apply(_ step: TerminalTextSize.Step) -> Bool {
        guard let next = TerminalTextSize.applying(step, to: points) else { return false }
        points = next
        TerminalTextSize.store(next, box: box, in: defaults)
        announce(TerminalTextSize.announcement(next))
        return true
    }
}
