// SPDX-License-Identifier: Apache-2.0

import Foundation

/// One save in a box's history, as portenvd lists it (newest last or in any
/// order; the inspector sorts).
public struct SaveInfo: Equatable, Sendable, Identifiable {
    public let id: String
    public let time: Date
    /// autosave, point, release (a close) or orphaned.
    public let kind: String
    /// The machine that made it.
    public let machine: String

    public init(id: String, time: Date, kind: String, machine: String) {
        self.id = id
        self.time = time
        self.kind = kind
        self.machine = machine
    }
}

/// The inspector's words (GUIDELINES.md §6): it explains and offers the
/// obvious action; it never holds settings.
public enum InspectorText {
    // Section headings, always in this order.
    public static let whereItIs = "Where it is"
    public static let savesHeading = "Saves"
    public static let whosHere = "Who's here"
    public static let runningNow = "Running now"

    /// "Running on <place>": This Mac, or the server's name.
    public static func runningOn(_ server: String?) -> String { "Running on \(server ?? "This Mac")" }

    /// "Saves go to <place>".
    public static func savesGoTo(_ place: String) -> String { "Saves go to \(place)" }

    /// "Since <time>": when the box opened (with the day if not today).
    public static func since(_ opened: Date, now: Date = Date(), calendar: Calendar = .current) -> String {
        return "Since \(clock(opened, now: now, calendar: calendar))"
    }

    /// A time as the inspector writes it: "09:12" today (two-digit hours, in
    /// the person's 12- or 24-hour setting), with the day otherwise.
    public static func clock(_ date: Date, now: Date = Date(), calendar: Calendar = .current) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).hour(.twoDigits(amPM: .abbreviated)).minute(.twoDigits)
        if !calendar.isDate(date, inSameDayAs: now) { f = f.day().month(.abbreviated) }
        f.calendar = calendar
        f.locale = calendar.locale ?? .current
        return date.formatted(f)
    }

    /// Saves are encrypted where the box runs, before they leave it.
    public static func encryption(_ server: String?) -> String {
        "Encrypted on \(server ?? "this Mac") before they leave it"
    }

    /// Packages from apt-packages.txt that couldn't be installed, as the
    /// view shows them next to its Retry button; nil when none (§6).
    public static func packagesLine(_ names: [String]) -> String? {
        switch names.count {
        case 0: nil
        case 1: "1 package couldn't be installed: \(names[0])"
        default: "\(names.count) packages couldn't be installed: \(names.joined(separator: ", "))"
        }
    }

    /// The same line as plain text, with its action: "… · Retry".
    public static func packages(_ failed: [String]) -> String? {
        packagesLine(failed).map { "\($0) · Retry" }
    }

    public static func moving(to place: String) -> String { "Moving to \(place)" }
    public static let moveNote =
        "You can keep reading. Typing comes back when the box is running there. If the move stops, the box stays here, saved."

    /// A save's kind in plain words. GUIDELINES §6 lists the kinds; a kind
    /// this app doesn't know yet still reads as a save.
    public static func kind(_ raw: String, machine: String) -> String {
        switch raw {
        case "autosave": "Autosave"
        case "point": "Save point"
        case "release": "Saved when the box closed"
        case "orphaned": "Kept from \(machine)"
        default: "Save"
        }
    }

    /// One row of the Saves section.
    public struct SaveRow: Equatable, Sendable, Identifiable {
        public let id: String
        public let time: Date
        public let kind: String
        /// Hovering shows Revert (only the newest save point: Revert To ▸
        /// Last Save Point; other saves come with Browse Saves, 1.4).
        public let revertable: Bool
    }

    /// The five most recent saves, newest first.
    public static func saveRows(_ saves: [SaveInfo]) -> [SaveRow] {
        let newest = saves.sorted { $0.time > $1.time }
        let lastPoint = newest.first { $0.kind == "point" }?.id
        return newest.prefix(5).map {
            SaveRow(id: $0.id, time: $0.time, kind: kind($0.kind, machine: $0.machine), revertable: $0.id == lastPoint)
        }
    }

    /// portenvd lists a server box's saves on the server (ListSaves).
    public static func savesElsewhere(_ server: String) -> String {
        "Saves are listed on \(server) while the box runs there."
    }
    public static let noSaves = "No saves yet."

    public static let you = "You"
    /// Agents arrive in 2.4; until then the section is you, and this line.
    public static let noAgents = "No agents connected."

    /// One row of Running now: a tab and what runs in it.
    public struct TabRow: Equatable, Sendable, Identifiable {
        public let id: String
        public let name: String
        public let detail: String
    }

    public static func tabRows(_ tabs: [TabInfo]) -> [TabRow] {
        tabs.map { TabRow(id: $0.id, name: $0.name, detail: $0.program.isEmpty ? "Idle" : $0.program) }
    }
}

/// Finds commands that finished, from the tabs as the box agent reports
/// them: a tab's foreground program ending (or giving way to another) is a
/// command that finished, with how long it ran. A closed tab is not.
public struct CommandWatch: Sendable {
    public struct Finished: Equatable, Sendable {
        public let tab: String
        public let program: String
        public let seconds: Int
    }

    private var running: [String: (program: String, since: Date)] = [:]

    public init() {}

    public mutating func update(_ tabs: [TabInfo], at now: Date) -> [Finished] {
        var done: [Finished] = []
        var next: [String: (program: String, since: Date)] = [:]
        for t in tabs {
            let was = running[t.id]
            if let was, was.program != t.program {
                done.append(Finished(tab: t.name, program: was.program,
                                     seconds: Int(now.timeIntervalSince(was.since).rounded())))
            }
            if !t.program.isEmpty {
                next[t.id] = (was?.program == t.program) ? was! : (t.program, now)
            }
        }
        running = next
        return done
    }
}

/// When a notification is worth sending (GUIDELINES.md §5): only when the
/// window isn't in front, past each event's threshold.
public enum NotifyRules {
    public static let commandThreshold = 30
    public static let moveThreshold = 10

    public static func commandFinished(seconds: Int, windowInFront: Bool) -> Bool {
        !windowInFront && seconds > commandThreshold
    }

    public static func moveFinished(seconds: Int, windowInFront: Bool) -> Bool {
        !windowInFront && seconds > moveThreshold
    }
}

/// Notification texts (§5): title "<box> · <event>", the result in one line,
/// and a subtitle with who started it and where.
public enum NotifyText {
    public struct Content: Equatable, Sendable {
        public let title: String
        public let body: String
        public let subtitle: String
    }

    public static func commandFinished(box: String, tab: String, program: String, seconds: Int, location: String?) -> Content {
        Content(title: "\(box) · \(program) finished",
                body: "In the \(tab) tab, after \(duration(seconds)).",
                subtitle: "You · \(location ?? "This Mac")")
    }

    public static func moveFinished(box: String, to place: String, seconds: Int) -> Content {
        Content(title: "\(box) · Moved to \(place)",
                body: "The move took \(duration(seconds)). The box is running there.",
                subtitle: "You · \(place)")
    }

    /// "9 s", "1 min 35 s", "1 h 2 min".
    public static func duration(_ seconds: Int) -> String {
        let h = seconds / 3600, m = (seconds % 3600) / 60, s = seconds % 60
        if h > 0 { return m > 0 ? "\(h) h \(m) min" : "\(h) h" }
        if m > 0 { return s > 0 ? "\(m) min \(s) s" : "\(m) min" }
        return "\(s) s"
    }
}

/// Everything the inspector shows, as plain values (built from the window's
/// controllers, or from fixtures for renders).
public struct InspectorContent: Sendable {
    /// The server the box runs on; nil for this Mac.
    public var place: String?
    /// Where the box is moving to, during a move.
    public var movingTo: String?
    public var storagePlace: String?
    public var openedAt: Date?
    public var failedPackages: [String]
    public var savedAt: Date?
    public var saves: [InspectorText.SaveRow]
    /// Set when the saves are listed elsewhere (a box on a server).
    public var savesElsewhere: String?
    public var tabs: [InspectorText.TabRow]

    public init(place: String? = nil, movingTo: String? = nil, storagePlace: String? = nil, openedAt: Date? = nil,
                failedPackages: [String] = [], savedAt: Date? = nil, saves: [InspectorText.SaveRow] = [],
                savesElsewhere: String? = nil, tabs: [InspectorText.TabRow] = []) {
        self.place = place
        self.movingTo = movingTo
        self.storagePlace = storagePlace
        self.openedAt = openedAt
        self.failedPackages = failedPackages
        self.savedAt = savedAt
        self.saves = saves
        self.savesElsewhere = savesElsewhere
        self.tabs = tabs
    }

    @MainActor
    public init(box: BoxController, tabs: TabsController) {
        let server: String? = if case .server(let s) = box.location { ServerName.display(s) } else { nil }
        self.init(place: server,
                  movingTo: box.movingTo.map(ServerName.display),
                  storagePlace: box.state?.storagePlace,
                  openedAt: box.state?.openedAt,
                  failedPackages: box.state?.failedPackages ?? [],
                  savedAt: box.state?.savedAt,
                  saves: InspectorText.saveRows(box.saves),
                  savesElsewhere: server.map(InspectorText.savesElsewhere),
                  tabs: InspectorText.tabRows(tabs.tabs))
    }
}
