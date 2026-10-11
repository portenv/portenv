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

    /// Closing the last box window: the box is saved and released before
    /// the list shows (WindowClose), so the wait reads as work (R-0061).
    public static let closing = "Saving before closing…"

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

/// §3.1: one rule for when the box was saved, from one recorded time (the
/// box state's savedAt), for both the state line and the inspector's "Last
/// saved": "just now" for the first minute, then the time; from the next
/// day "yesterday at" the time, then the date and the time (R-0061).
public enum SavedTime {
    public static let justNowSeconds: TimeInterval = 60

    static func isJustNow(_ at: Date, now: Date) -> Bool { now.timeIntervalSince(at) < justNowSeconds }

    /// "Saved just now", "Saved at 14:58", "Saved yesterday at 23:43",
    /// "Saved on 9 Oct at 23:43" (another year: "on 9 Oct 2025 at 23:43").
    public static func stateLine(_ at: Date, now: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        if isJustNow(at, now: now) { return "Saved just now" }
        let words = when(at, now: now, timeZone: timeZone, locale: locale)
        return "Saved " + (day(at, now: now, calendar: calendar(timeZone, locale)) == .today ? "at " + words : words)
    }

    /// "Last saved just now", "Last saved 14:58", "Last saved yesterday at
    /// 23:43", "Last saved on 9 Oct at 23:43" (GUIDELINES §6).
    public static func inspector(_ at: Date, now: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        "Last saved " + (isJustNow(at, now: now) ? "just now" : when(at, now: now, timeZone: timeZone, locale: locale))
    }

    /// When the words change next, so a view redraws then: "just now"
    /// becomes the time, midnight makes today's time "yesterday at" and
    /// yesterday's the date, and a new year adds the year. Nil once they no
    /// longer change.
    public static func nextChange(_ at: Date?, now: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> Date? {
        guard let at else { return nil }
        if isJustNow(at, now: now) { return at.addingTimeInterval(justNowSeconds) }
        let cal = calendar(timeZone, locale)
        switch day(at, now: now, calendar: cal) {
        case .today, .yesterday:
            return cal.date(byAdding: .day, value: 1, to: cal.startOfDay(for: now))
        case .thisYear:
            return cal.dateInterval(of: .year, for: now).map(\.end)
        case .earlier:
            return nil
        }
    }

    enum Day: Equatable { case today, yesterday, thisYear, earlier }

    /// Which day the save was on, seen from now, by the calendar's days (a
    /// save that looks later than now, from a clock ahead, counts as today).
    static func day(_ at: Date, now: Date, calendar cal: Calendar) -> Day {
        let saved = cal.startOfDay(for: at), today = cal.startOfDay(for: now)
        if saved >= today { return .today }
        if let y = cal.date(byAdding: .day, value: -1, to: today), saved >= y { return .yesterday }
        return cal.isDate(at, equalTo: now, toGranularity: .year) ? .thisYear : .earlier
    }

    /// The time, then with "yesterday at" or "on <date> at" before it.
    static func when(_ at: Date, now: Date, timeZone: TimeZone, locale: Locale) -> String {
        let clock = StateLine.clock(at, timeZone: timeZone, locale: locale)
        let cal = calendar(timeZone, locale)
        let d = day(at, now: now, calendar: cal)
        switch d {
        case .today: return clock
        case .yesterday: return "yesterday at " + clock
        case .thisYear, .earlier:
            let f = DateFormatter()
            f.locale = locale
            f.timeZone = timeZone
            f.calendar = cal
            f.setLocalizedDateFormatFromTemplate(d == .thisYear ? "dMMM" : "dMMMyyyy")
            return "on " + f.string(from: at) + " at " + clock
        }
    }

    static func calendar(_ timeZone: TimeZone, _ locale: Locale) -> Calendar {
        var c = locale.calendar
        c.timeZone = timeZone
        return c
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
                state = SavedTime.stateLine(at, now: now, timeZone: timeZone, locale: locale)
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

/// What the title shows: the state line, its symbol and whether the symbol
/// spins, taken together so they always describe the same state.
public struct TitleLook: Equatable, Sendable {
    public var line: String
    public var symbol: String?
    public var spins: Bool

    /// A symbol spins only while the line says something is in progress.
    public var consistent: Bool {
        let inProgress = line.contains("Saving…") || line.contains("Moving to") || line.contains("Opening…")
            || line == StateLine.closing || line.hasSuffix("· retrying")
        return spins == inProgress && (!spins || symbol == "arrow.triangle.2.circlepath")
    }
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

/// §4.5: the terminal's layout.
public enum TerminalLayout {
    /// Space between the terminal's text and the window's edges, in points,
    /// painted in the terminal's background.
    public static let margin: CGFloat = 8
}

/// §12: what VoiceOver says for the window's controls.
public enum A11y {
    public static func titleLabel(box: String) -> String { "\(box), box menu" }

    /// The recovery key read one character at a time, group by group, so it
    /// can be written down: "Recovery key: K 7 Q F, M 2 X A, …".
    public static func recoveryKey(_ key: String) -> String {
        let groups = key.split(separator: "-").map { $0.map(String.init).joined(separator: " ") }
        return "Recovery key: " + groups.joined(separator: ", ")
    }

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

    /// The tab bar (§3, §4.1): each tab says its name and that it's a tab;
    /// the one the terminal shows is marked selected by the view.
    public static func tab(_ name: String) -> String { "\(name) tab" }
    public static let tabBar = "Tabs"
    public static let newTab = "New tab"
    public static func closeTab(_ name: String) -> String { "Close \(name) tab" }
    public static func renameTab(_ name: String) -> String { "Name of the \(name) tab" }

    /// The inspector (§6) and its controls.
    public static let inspector = "Inspector"
    public static let showInspector = "Show inspector"
    public static let hideInspector = "Hide inspector"
    public static let retryPackages = "Retry installing packages"
    public static func revertTo(_ kind: String) -> String { "Revert to this \(kind)" }
    /// A save row's accessibility actions: Revert, for the newest save point
    /// (shown on hover; an action too, for VoiceOver and the keyboard).
    public static func saveRowActions(_ row: InspectorText.SaveRow) -> [String] {
        row.revertable ? [revertTo(row.kind)] : []
    }
    /// A tab in Running now, and what runs in it.
    public static func runningTab(_ name: String, program: String?) -> String {
        program.map { "\(name) tab, running \($0)" } ?? "\(name) tab, idle"
    }
}
