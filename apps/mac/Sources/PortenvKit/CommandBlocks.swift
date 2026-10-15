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
    /// The header's trailing status: "✓ 0.8 s · 14:20", "✗ exit 1 · 8.4 s ·
    /// 14:31", "running 3 min · since 14:37". Times the app didn't see are
    /// left out, never guessed. Times follow the person's 12/24-hour
    /// setting, as the state line's do; numbers, the app's language.
    public static func status(_ block: CommandBlock, now: Date, timeZone: TimeZone = .current, clockLocale: Locale = .current,
                              locale: Locale = SettingsText.appLocale()) -> String {
        let clock = { (d: Date) in StateLine.clock(d, timeZone: timeZone, locale: clockLocale) }
        switch block.state {
        case .running:
            var parts = ["running"]
            if let s = block.startedAt {
                parts.append(elapsed(now.timeIntervalSince(s), locale: locale))
                parts.append("since " + clock(s))
            }
            return parts.joined(separator: " · ")
        case .succeeded, .failed:
            var parts: [String] = []
            if case let .failed(code) = block.state { parts.append("exit \(code)") }
            if let s = block.startedAt, let e = block.endedAt { parts.append(elapsed(e.timeIntervalSince(s), locale: locale)) }
            if let e = block.endedAt { parts.append(clock(e)) }
            return parts.joined(separator: " · ")
        }
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
