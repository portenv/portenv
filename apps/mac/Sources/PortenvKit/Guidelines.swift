// SPDX-License-Identifier: Apache-2.0

import Foundation

// The rules of docs/design/GUIDELINES.md that the app's text and menus
// follow, kept here so each one has a test.

/// §2: servers are shown by name: the host name without the user (until
/// servers can be named), never user@IP.
public enum ServerName {
    public static func display(_ target: String) -> String {
        if target == "this-mac" { return "This Mac" }
        if let at = target.lastIndex(of: "@") { return String(target[target.index(after: at)...]) }
        return target
    }
}

/// §3.1: texts of the state line that don't come from the save state.
public enum StateLine {
    /// Transient messages (revert, move finished) last this long.
    public static let transientSeconds = 5

    public static func moving(to target: String) -> String { "Moving to \(ServerName.display(target))…" }

    public static func moved(to target: String) -> String { "Moved to \(ServerName.display(target))" }

    public static func reverted(to time: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        "Reverted to \(clock(time, timeZone: timeZone, locale: locale)) · your changes were kept"
    }

    /// Local wall-clock time in the user's 12/24-hour setting, never UTC.
    static func clock(_ date: Date, timeZone: TimeZone, locale: Locale) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = timeZone
        f.dateStyle = .none
        f.timeStyle = .short
        return f.string(from: date)
    }
}

extension BoxState {
    /// §3.1: the state line, `<Location> · <Save state>`, from this state
    /// alone (recorded facts only).
    public func line(now: Date = Date(), timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        let clock = { (d: Date) in StateLine.clock(d, timeZone: timeZone, locale: locale) }
        let notSavedSince = savedAt.map { "Not saved since " + clock($0) } ?? "Not saved"
        let state: String
        switch save {
        case .notSavedYet: state = "Not saved yet"
        case .saving: state = "Saving…"
        case .saved:
            if let at = savedAt {
                state = now.timeIntervalSince(at) < 60 ? "Saved just now" : "Saved at " + clock(at)
            } else {
                state = "Saved"
            }
        case .offline: state = "Offline · will save later"
        case .agentUnavailable, .notSaved: state = notSavedSince
        case .retrying: state = notSavedSince + " · retrying"
        case .quitUnsaved: state = notSavedSince + " · Portenv quit before saving"
        case .closed: state = "Closed"
        }
        return "\(location.map(ServerName.display) ?? "This Mac") · \(state)"
    }

    /// §3.1: the sync symbol next to the title (SF Symbols), or none.
    public var symbol: String? {
        switch save {
        case .notSavedYet, .closed: return nil
        case .saving, .retrying: return "arrow.triangle.2.circlepath"
        case .saved: return "checkmark.circle"
        case .offline: return "icloud.slash"
        case .agentUnavailable, .notSaved, .quitUnsaved: return "exclamationmark.triangle"
        }
    }

    /// Whether the symbol animates (never with Reduce Motion: the view checks).
    public var symbolSpins: Bool { save == .saving || save == .retrying }
}

/// One item of the box's menu (§3.2): the title menu and the menu bar's Box
/// menu are built from the same list, in the same order. "—" is a separator.
public struct BoxMenuItem: Equatable, Sendable {
    public enum Action: Equatable, Sendable {
        case none, move(String), revertToLastSavePoint, makeSavePoint, restartBox
    }
    public var title: String
    public var enabled = true
    public var checked = false
    public var shortcut: String?
    public var action: Action = .none
    public var submenu: [BoxMenuItem] = []

    public static let separator = BoxMenuItem(title: "—", enabled: false)
    public var isSeparator: Bool { title == "—" }
}

public enum BoxMenuModel {
    /// The guideline order: Rename…, Move To ▸, (Duplicate for a Task… once
    /// it exists), —, Revert To ▸, Make a Save Point ⌘S, —, Restart Box
    /// (when the box agent is unavailable, or with ⌥), Show in Finder ⌥⌘R.
    /// Items that can't work yet are disabled, with no explanation here.
    public static func items(open: Bool, busy: Bool, agentUnavailable: Bool, optionHeld: Bool,
                             location: String?, servers: [String]) -> [BoxMenuItem] {
        let act = open && !busy
        var move = [BoxMenuItem(title: "This Mac", enabled: !busy, checked: location == "this-mac", action: .move("this-mac"))]
        if !servers.isEmpty { move.append(.separator) }
        for s in servers {
            move.append(BoxMenuItem(title: ServerName.display(s), enabled: !busy, checked: location == s, action: .move(s)))
        }
        move.append(.separator)
        move.append(BoxMenuItem(title: "Add a Server…", enabled: false))
        var items: [BoxMenuItem] = [
            BoxMenuItem(title: "Rename…", enabled: false),
            BoxMenuItem(title: "Move To", enabled: !busy, submenu: move),
            .separator,
            BoxMenuItem(title: "Revert To", enabled: act, submenu: [
                BoxMenuItem(title: "Last Save Point", enabled: act, action: .revertToLastSavePoint),
                BoxMenuItem(title: "Browse All Saves…", enabled: false),
            ]),
            BoxMenuItem(title: "Make a Save Point", enabled: act, shortcut: "⌘S", action: .makeSavePoint),
            .separator,
        ]
        if agentUnavailable || optionHeld {
            items.append(BoxMenuItem(title: "Restart Box", enabled: act, action: .restartBox))
        }
        items.append(BoxMenuItem(title: "Show in Finder", enabled: false, shortcut: "⌥⌘R"))
        return items
    }
}

/// §12: what VoiceOver says for the window's controls.
public enum A11y {
    public static func titleLabel(box: String) -> String { "\(box), box menu" }

    public static func symbolLabel(_ state: BoxState?) -> String {
        switch state?.save {
        case .saving, .retrying: return "Saving"
        case .saved: return "Saved"
        case .offline: return "Offline"
        case .agentUnavailable, .notSaved, .quitUnsaved: return "Not saved"
        default: return "Not saved yet"
        }
    }

    public static func terminal(box: String) -> String { "Terminal in \(box)" }
}
