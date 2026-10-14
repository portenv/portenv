// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Where a workspace is open when it isn't this Mac (portenvd's
/// GetPlacement; 2.3). Servers come by ID and name, never by address.
public struct Placement: Equatable, Sendable {
    public enum Holder: Equatable, Sendable {
        case none
        case knownServer(id: String, name: String)
        /// Another Mac, or a server this Mac doesn't know: its machine ID.
        case otherMachine(id: String)
    }

    public enum Answers: Equatable, Sendable {
        case yes, no
        /// Portenv can't ask it (another Mac).
        case cannotAsk
    }

    public let holder: Holder
    public let newestSaveAt: Date?
    /// nil while the probe hasn't answered yet.
    public let answers: Answers?

    public init(holder: Holder, newestSaveAt: Date?, answers: Answers?) {
        self.holder = holder
        self.newestSaveAt = newestSaveAt
        self.answers = answers
    }

    /// The place's name, as people read it.
    public var placeName: String {
        switch holder {
        case .none: "this Mac"
        case let .knownServer(_, name): name
        case let .otherMachine(id): id
        }
    }
}

/// The sheet shown when a workspace is open somewhere else (the board's
/// "Opening a workspace that's open somewhere else" and "The other place
/// isn't answering"). The person never meets the word "lease". "Look
/// without changing anything" is hidden until 1.4's Browse Saves (R-0140).
public struct LeaseSheet: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The probe hasn't answered: only Cancel.
        case checking
        /// It's open on a server that answers.
        case openThere
        /// The place doesn't answer, or can't be asked.
        case notAnswering
    }

    public enum Action: Equatable, Sendable {
        case connect
        /// Saves and stops it there, then opens it here (MoveBox).
        case moveHere
        /// Opens it here without the other place (resume rule 4).
        case moveHereTakingOver
        case keepWaiting
    }

    public struct Choice: Equatable, Sendable {
        public let action: Action
        /// The option's title (a row on the open-there sheet).
        public let title: String
        /// The line under it.
        public let detail: String?
        /// The button's words.
        public let button: String
    }

    public struct Warning: Equatable, Sendable {
        public let title: String
        public let text: String
    }

    public let kind: Kind
    /// The place's name (a server's name, or another Mac's ID).
    public let place: String
    public let title: String
    public let body: String
    /// "Checking whether … answers…" while checking.
    public let detail: String?
    public let warning: Warning?
    public let footnote: String?
    public let choices: [Choice]
    /// What Return does (nil: only Cancel).
    public let defaultAction: Action?

    /// R-0168: Move is the default only for a known server whose ping
    /// failed, and only while its newest save is under an hour old.
    public static let moveDefaultWithin: TimeInterval = 3600

    public static let otherMacLine = "Another Mac may still be working in it. Anything it hasn\u{2019}t saved yet stays there, and is kept as a separate save when it next saves."

    public init(box: String, placement: Placement, now: Date,
                clock: ((Date) -> String)? = nil) {
        let clock = clock ?? { StateLine.clock($0, timeZone: .current, locale: .current) }
        let place = placement.placeName
        self.place = place
        let age = placement.newestSaveAt.map { now.timeIntervalSince($0) }
        let saveLine: String = {
            guard let at = placement.newestSaveAt, let age else { return "" }
            return "\(clock(at)), \(Self.minutes(age)) ago"
        }()
        switch (placement.holder, placement.answers) {
        case (.knownServer, nil):
            kind = .checking
            title = "\(box) is open on \(place)"
            body = "A workspace runs in one place at a time, so its saves never fork."
            detail = "Checking whether \(place) answers\u{2026}"
            warning = nil
            footnote = nil
            choices = []
            defaultAction = nil
        case (.knownServer, .yes?):
            kind = .openThere
            title = "\(box) is open on \(place)"
            body = "It\u{2019}s running there now. A workspace runs in one place at a time, so its saves never fork."
            detail = nil
            warning = nil
            footnote = nil
            choices = [
                Choice(action: .connect, title: "Connect to it there",
                       detail: "Work in it on \(place) from this window. Nothing stops.", button: "Connect"),
                Choice(action: .moveHere, title: "Move it to this Mac",
                       detail: "Saves it there, stops it there, and starts it here.", button: "Move"),
            ]
            defaultAction = .connect
        default:
            kind = .notAnswering
            let cannotAsk = placement.answers == .cannotAsk || { if case .otherMachine = placement.holder { return true }; return false }()
            title = cannotAsk ? "\(box) is open on \(place)" : "\(place) isn\u{2019}t answering"
            if cannotAsk {
                body = saveLine.isEmpty ? "Portenv can\u{2019}t check whether that Mac is still working in it."
                    : "Its newest save is from \(saveLine). Portenv can\u{2019}t check whether that Mac is still working in it."
            } else {
                body = saveLine.isEmpty ? "\(box) was last open there."
                    : "\(box) was last open there. Its newest save on \(place) is from \(saveLine)."
            }
            detail = nil
            let from = placement.newestSaveAt.map { "it starts from the \(clock($0)) save" } ?? "it starts from its newest save"
            warning = Warning(title: "If you move it to this Mac now:",
                              text: "\(from). Anything done on \(place) after that isn\u{2019}t lost: when \(place) comes back, Portenv keeps it as a separate save you can open.")
            footnote = cannotAsk ? nil : "Or wait: Portenv keeps trying \(place) and tells you when it answers."
            choices = [
                Choice(action: .keepWaiting, title: "Keep Waiting", detail: nil, button: "Keep Waiting"),
                Choice(action: .moveHereTakingOver, title: "Move to This Mac",
                       detail: cannotAsk ? Self.otherMacLine : nil, button: "Move to This Mac"),
            ]
            // R-0168: never Move by default for a holder that can't be asked.
            let recent = (age ?? .infinity) < Self.moveDefaultWithin
            defaultAction = !cannotAsk && recent ? .moveHereTakingOver : .keepWaiting
        }
    }

    static func minutes(_ seconds: TimeInterval) -> String {
        let m = max(1, Int((seconds / 60).rounded()))
        if m < 90 { return m == 1 ? "1 minute" : "\(m) minutes" }
        let h = Int((Double(m) / 60).rounded())
        return h == 1 ? "1 hour" : "\(h) hours"
    }
}

/// The Move confirmation (the board's "Move To ▸ … · the confirmation"):
/// every running command that will stop, or "Nothing is running".
public struct MoveConfirmation: Equatable, Sendable {
    public let title: String
    public let body: String
    /// "<command> in the <tab> tab · 3 min", one per running command. The
    /// command is shown on this Mac only (R-0140).
    public let running: [String]
    public let runningTitle: String
    public let target: String

    public static let footer = "Tabs, files and saved commands come along. Agents connected here are disconnected and can reconnect there."

    public init(box: String, target: String, tabs: [TabInfo], now: Date) {
        self.target = target
        let place = ServerName.display(target)
        title = "Move \(box) to \(place)?"
        body = target == "this-mac"
            ? "Portenv saves it where it runs, stops it there, and starts it on this Mac from that save. It takes about a minute; you can keep reading while it moves."
            : "Portenv saves it here, stops it on this Mac, and starts it on \(place) from that save. It takes about a minute; you can keep reading while it moves."
        running = tabs.filter { !$0.command.isEmpty }.map { t in
            let minutes = t.runningSince.map { " \u{00B7} \(max(1, Int((now.timeIntervalSince($0) / 60).rounded()))) min" } ?? ""
            return "\(t.command) in the \(t.name) tab\(minutes)"
        }
        switch running.count {
        case 0: runningTitle = "Nothing is running."
        case 1: runningTitle = "1 command is still running and will stop:"
        default: runningTitle = "\(running.count) commands are still running and will stop:"
        }
    }
}
