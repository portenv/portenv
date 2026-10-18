// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Command blocks (the command-blocks board, R-0139; ADR 0020): every
/// command in a tab is a block, built from the tab's OSC 133 marks. Finished
/// blocks show as cards read back from the terminal's own buffer, and the
/// running one stays the live terminal (slice 0, R-0174).
public struct CommandBlock: Equatable, Sendable, Identifiable {
    public enum State: Equatable, Sendable {
        case running
        case succeeded
        /// A command that ended with a non-zero exit code.
        case failed(exit: Int)
    }

    /// The command-start row (scroll-invariant): a block's stable identity
    /// while it's in the buffer.
    public let id: Int
    /// The command as typed, from its prompt line.
    public let command: String
    public let state: State
    /// The output's rows (scroll-invariant), or nil when there's none yet.
    public let output: ClosedRange<Int>?
    /// When it started and ended, as this app saw the marks arrive; nil for
    /// commands replayed in a seed.
    public let startedAt: Date?
    public let endedAt: Date?

    public init(id: Int, command: String, state: State, output: ClosedRange<Int>?, startedAt: Date? = nil, endedAt: Date? = nil) {
        self.id = id
        self.command = command
        self.state = state
        self.output = output
        self.startedAt = startedAt
        self.endedAt = endedAt
    }

    public var outputLines: Int { output?.count ?? 0 }
}

public enum CommandBlocks {
    /// Builds the blocks from a tab's marks. `prompt` gives the command typed
    /// on a prompt row (what follows the prompt's B mark); `cursorRow` is
    /// where a running command's output reaches so far. A command-start
    /// mark with no prompt before it still makes a block (its command is
    /// unknown, so it's empty).
    public static func blocks(marks: [OverlayRows.Mark], cursorRow: Int, prompt: (Int) -> String?) -> [CommandBlock] {
        let sorted = marks.sorted { $0.row < $1.row }
        var out: [CommandBlock] = []
        for (i, m) in sorted.enumerated() where m.kind == .commandStart {
            let promptRow = sorted[..<i].last { $0.kind == .prompt }?.row
            let command = promptRow.flatMap(prompt) ?? ""
            let next = sorted[(i + 1)...].first { mark in
                if case .commandEnd = mark.kind { return true }
                return mark.kind == .prompt || mark.kind == .commandStart
            }
            let state: CommandBlock.State
            let last: Int
            var endedAt: Date?
            switch next?.kind {
            case let .commandEnd(exit)?:
                state = (exit ?? 0) == 0 ? .succeeded : .failed(exit: exit ?? 0)
                last = next!.row - 1
                endedAt = next!.at
            case .some:
                // A new prompt with no end mark: the shell lost it (a
                // Ctrl-C at the prompt, say). It ended, with no code.
                state = .succeeded
                last = next!.row - 1
            case nil:
                state = .running
                last = cursorRow
            }
            out.append(CommandBlock(id: m.row, command: command, state: state,
                                    output: last >= m.row ? m.row ... last : nil,
                                    startedAt: m.at, endedAt: endedAt))
        }
        return out
    }
}

/// How a block shows (R-0174).
public enum BlockLayout {
    /// The lines kept at each end of a long expanded block.
    public static let keep = 20

    /// A finished success folds to its header by default, however long; a
    /// failure and a running block open by themselves. A click toggles.
    public static func openByDefault(_ state: CommandBlock.State) -> Bool {
        if case .succeeded = state { return false }
        return true
    }

    public enum Shown: Equatable, Sendable {
        /// Only the header.
        case folded
        /// Every output row.
        case all(ClosedRange<Int>)
        /// The first and last `keep` rows, with the count between hidden.
        case headTail(head: ClosedRange<Int>, hidden: Int, tail: ClosedRange<Int>)
    }

    /// What an open or folded block shows: an open long block keeps its
    /// first and last 20 lines until Show All Output.
    public static func shown(_ block: CommandBlock, open: Bool, showAll: Bool) -> Shown {
        guard open, let rows = block.output else { return .folded }
        if showAll || rows.count <= 2 * keep { return .all(rows) }
        let head = rows.lowerBound ... rows.lowerBound + keep - 1
        let tail = rows.upperBound - keep + 1 ... rows.upperBound
        return .headTail(head: head, hidden: rows.count - 2 * keep, tail: tail)
    }
}

/// A block's words (the command-blocks board), in the app's language.
public enum BlockText {
    /// The header's trailing status: "0.8 s · 14:20", "exit 1 · 8.4 s ·
    /// 14:31", "running · 3 min" (when it started is in `since`, for the
    /// tooltip and VoiceOver, not said twice; R-0207). Times the app didn't see are
    /// left out, never guessed. Times follow the person's 12/24-hour
    /// setting, as the state line's do; numbers, the app's language.
    public static func status(_ block: CommandBlock, now: Date, timeZone: TimeZone = .current, clockLocale: Locale = .current,
                              locale: Locale = SettingsText.appLocale()) -> String {
        let clock = { (d: Date) in StateLine.clock(d, timeZone: timeZone, locale: clockLocale) }
        switch block.state {
        case .running:
            var parts = ["running"]
            if let s = block.startedAt { parts.append(elapsed(now.timeIntervalSince(s), locale: locale)) }
            return parts.joined(separator: " · ")
        case .succeeded, .failed:
            var parts: [String] = []
            if case let .failed(code) = block.state { parts.append("exit \(code)") }
            if let s = block.startedAt, let e = block.endedAt { parts.append(elapsed(e.timeIntervalSince(s), locale: locale)) }
            if let e = block.endedAt { parts.append(clock(e)) }
            return parts.joined(separator: " · ")
        }
    }

    /// "since 14:37" for a running block the app saw start: its header's
    /// tooltip and VoiceOver label; nil otherwise.
    public static func since(_ block: CommandBlock, timeZone: TimeZone = .current, clockLocale: Locale = .current) -> String? {
        guard block.state == .running, let s = block.startedAt else { return nil }
        return "since " + StateLine.clock(s, timeZone: timeZone, locale: clockLocale)
    }

    /// What VoiceOver reads for a block's header, a button (R-0207): "npm
    /// run build, succeeded, 41 seconds". Its expanded or collapsed state is
    /// the button's value (`expandedState`).
    public static func spoken(_ block: CommandBlock, now: Date, timeZone: TimeZone = .current, clockLocale: Locale = .current) -> String {
        var parts = [block.command.isEmpty ? "command" : block.command]
        switch block.state {
        case .succeeded: parts.append("succeeded")
        case let .failed(code): parts += ["failed", "exit \(code)"]
        case .running: parts.append("running")
        }
        if block.state == .running, let s = block.startedAt {
            parts.append(spokenElapsed(now.timeIntervalSince(s)))
            parts.append(since(block, timeZone: timeZone, clockLocale: clockLocale) ?? "")
        } else if let s = block.startedAt, let e = block.endedAt {
            parts.append(spokenElapsed(e.timeIntervalSince(s)))
        }
        return parts.filter { !$0.isEmpty }.joined(separator: ", ")
    }

    public static func expandedState(open: Bool) -> String { open ? "expanded" : "collapsed" }

    /// Elapsed time as it's said: "0.8 seconds", "1 second", "41 seconds",
    /// "3 minutes", "2 hours 5 minutes".
    public static func spokenElapsed(_ seconds: TimeInterval) -> String {
        let s = max(0, seconds)
        func unit(_ n: Int, _ one: String) -> String { "\(n) \(one)\(n == 1 ? "" : "s")" }
        if s < 10 {
            let tenths = (s * 10).rounded() / 10
            return tenths == 1 ? "1 second" : String(format: "%.1f seconds", tenths).replacingOccurrences(of: ".0 ", with: " ")
        }
        if s < 60 { return unit(Int(s), "second") }
        let m = Int(s / 60)
        if m < 60 { return unit(m, "minute") }
        return unit(m / 60, "hour") + (m % 60 == 0 ? "" : " " + unit(m % 60, "minute"))
    }

    /// "0.8 s", "8.4 s", "41 s", "3 min", "2 h 5 min".
    public static func elapsed(_ seconds: TimeInterval, locale: Locale = SettingsText.appLocale()) -> String {
        let s = max(0, seconds)
        if s < 10 { return number(s, digits: 1, locale: locale) + " s" }
        if s < 60 { return number(s.rounded(.down), digits: 0, locale: locale) + " s" }
        let m = Int(s / 60)
        if m < 60 { return "\(m) min" }
        return "\(m / 60) h \(m % 60) min"
    }

    /// "… 2,398 lines hidden" (the count in the app's language).
    public static func hidden(_ count: Int, locale: Locale = SettingsText.appLocale()) -> String {
        "\u{2026} \(number(Double(count), digits: 0, locale: locale)) lines hidden"
    }

    public static let showAllOutput = "Show All Output"

    static func number(_ x: Double, digits: Int, locale: Locale) -> String {
        let f = NumberFormatter()
        f.locale = locale
        f.numberStyle = .decimal
        f.minimumFractionDigits = digits
        f.maximumFractionDigits = digits
        return f.string(from: NSNumber(value: x)) ?? String(x)
    }
}

/// A block's actions (the command-blocks board, R-0139, R-0171): Copy
/// Output, Copy Command and Run Again.
public enum BlockCopy {
    /// The output to copy: every line, and first, when the block outgrew
    /// the scrollback, a line saying so rather than copying less silently.
    public static func output(lines: [String], trimmed: Int, locale: Locale = SettingsText.appLocale()) -> String {
        let body = lines.joined(separator: "\n")
        guard trimmed > 0 else { return body }
        return trimmedNote(trimmed, locale: locale) + "\n" + body
    }

    /// "Output trimmed: the first 1,204 lines are past the scrollback."
    public static func trimmedNote(_ n: Int, locale: Locale = SettingsText.appLocale()) -> String {
        n == 1
            ? "Output trimmed: the first line is past the scrollback."
            : "Output trimmed: the first \(BlockText.number(Double(n), digits: 0, locale: locale)) lines are past the scrollback."
    }

    /// What Run Again types: the command, without control characters, and
    /// Return. Nil when there's no command to run (it left the scrollback).
    public static func runAgainInput(_ command: String) -> [UInt8]? {
        let clean = String(String.UnicodeScalarView(command.unicodeScalars.filter { $0.value >= 0x20 && $0.value != 0x7F }))
            .trimmingCharacters(in: .whitespaces)
        guard !clean.isEmpty else { return nil }
        return Array((clean + "\r").utf8)
    }

    /// Run Again asks first unless portenvd recorded that the person at this
    /// Mac ran the command: an agent's command, or one with no recorded
    /// author, never runs again on one click (R-0139).
    public static func asksBeforeRunningAgain(authorIsYou: Bool?) -> Bool { authorIsYou != true }

    /// The question: "Run “npm test” again?" with who ran it, if known.
    public static func runAgainQuestion(command: String, author: String?) -> (title: String, message: String) {
        let who = author.map { "\($0) ran it." } ?? "Portenv doesn\u{2019}t know who ran it."
        return ("Run \u{201C}\(command)\u{201D} again?", "\(who) It runs in this tab as you.")
    }

    /// Copy Both: the command as it was typed, after a prompt sign, then
    /// its output.
    public static func both(command: String, output: String) -> String {
        output.isEmpty ? "$ \(command)" : "$ \(command)\n\(output)"
    }

    public static let copyOutput = "Copy Output"
    public static let copyCommand = "Copy Command"
    public static let copyBoth = "Copy Both"
    public static let runAgain = "Run Again"
    public static let collapse = "Collapse"
    public static let expand = "Expand"
    public static let jumpToStart = "Jump to Start"
}

/// Moving between blocks (⌘↑ and ⌘↓, R-0139): the block above or below the
/// selected one, oldest first; with none selected, the newest (⌘↑) or the
/// oldest (⌘↓). Stops at the ends.
public enum BlockNavigation {
    public static func previous(from selected: Int?, in blocks: [CommandBlock]) -> Int? {
        let ids = blocks.map(\.id)
        guard let selected, let i = ids.firstIndex(of: selected) else { return ids.last }
        return i > 0 ? ids[i - 1] : selected
    }

    public static func next(from selected: Int?, in blocks: [CommandBlock]) -> Int? {
        let ids = blocks.map(\.id)
        guard let selected, let i = ids.firstIndex(of: selected) else { return ids.first }
        return i + 1 < ids.count ? ids[i + 1] : selected
    }
}

/// Find across the blocks (R-0139, R-0174): every block's command and every
/// output row, folded or hidden ones too, case-insensitively, in order. A
/// match in a folded block or in hidden rows opens them when it's shown.
public enum BlockFind {
    public struct Match: Equatable, Sendable {
        public let block: Int
        /// The output row, or nil for the block's command.
        public let row: Int?
        /// Where, in characters of that line.
        public let range: Range<Int>
    }

    public static func matches(_ query: String, in blocks: [CommandBlock], row text: (Int) -> String?) -> [Match] {
        var out: [Match] = []
        for b in blocks {
            out += matches(query, inCommandOf: b)
            if let rows = b.output { out += matches(query, block: b.id, rows: rows, row: text) }
        }
        return out
    }

    /// The matches in a block's command.
    public static func matches(_ query: String, inCommandOf block: CommandBlock) -> [Match] {
        scan(query, block.command).map { Match(block: block.id, row: nil, range: $0) }
    }

    /// The matches in some of a block's rows: the app searches a long block
    /// in chunks of `chunkRows`, letting the window draw between them.
    public static func matches(_ query: String, block: Int, rows: ClosedRange<Int>, row text: (Int) -> String?) -> [Match] {
        var out: [Match] = []
        for r in rows { if let t = text(r) { out += scan(query, t).map { Match(block: block, row: r, range: $0) } } }
        return out
    }

    /// Rows searched between two chances for the window to draw.
    public static let chunkRows = 250

    static func scan(_ query: String, _ line: String) -> [Range<Int>] {
        let q = query.lowercased()
        guard !q.isEmpty else { return [] }
        let lower = line.lowercased()
        // A character whose lower case is longer: compare as is.
        guard lower.count == line.count else { return ranges(of: query, in: line) }
        return ranges(of: q, in: lower)
    }

    static func ranges(of needle: String, in hay: String) -> [Range<Int>] {
        guard !needle.isEmpty, hay.count >= needle.count else { return [] }
        return Array(hay).ranges(of: Array(needle))
    }

    /// "3 of 12", or "Not found".
    public static func count(current: Int?, total: Int) -> String {
        guard total > 0 else { return "Not found" }
        return "\((current ?? 0) + 1) of \(total)"
    }
}
