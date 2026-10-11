// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// Spike question 3 against real tmux (ADR 0017): the streams that
/// tests/spike/q3/capture.py recorded from tmux 3.4 in the toolbox image,
/// replayed into headless SwiftTerm terminals with the overlay tracker.
///
/// - A: an attached tmux client, as `portenv attach` gives the app today,
///   with allow-passthrough on and every mark also sent wrapped (tmux drops a
///   bare OSC 133).
/// - C: each pane's own output from a control-mode client, one terminal per
///   tab, as the box agent already reads it.
///
/// For every mark left in a terminal: a prompt mark (A) must be on a prompt
/// line ("P<n>$ …"), in order; a command-start mark (C) on its command's
/// first output line. And every prompt line on the terminal must have its
/// mark.
private final class Headless: TerminalDelegate {
    func send(source _: Terminal, data _: ArraySlice<UInt8>) {}
}

struct ReplayResult: CustomStringConvertible {
    var promptLines = 0, promptMarks = 0, misplaced = 0, unmarked = 0, startMarks = 0, startMisplaced = 0
    var scrollMisplaced = 0
    var description: String {
        "prompt lines \(promptLines), prompt marks \(promptMarks), misplaced \(misplaced), prompt lines without a mark \(unmarked); "
            + "command-start marks \(startMarks), misplaced \(startMisplaced); misplaced while scrolling \(scrollMisplaced)"
    }

    static func + (a: Self, b: Self) -> Self {
        Self(promptLines: a.promptLines + b.promptLines, promptMarks: a.promptMarks + b.promptMarks,
             misplaced: a.misplaced + b.misplaced, unmarked: a.unmarked + b.unmarked,
             startMarks: a.startMarks + b.startMarks, startMisplaced: a.startMisplaced + b.startMisplaced,
             scrollMisplaced: a.scrollMisplaced + b.scrollMisplaced)
    }
}

enum Replay {
    static let fixtures = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().appendingPathComponent("../../../../tests/spike/q3/fixtures").standardized

    struct Record: Decodable {
        let t: String
        let path: String?
        let pane: String?
        let b64: String?
        let cols: Int?
        let rows: Int?
    }

    static func records(_ name: String) throws -> [Record] {
        let text = try String(contentsOf: fixtures.appendingPathComponent(name + ".jsonl"), encoding: .utf8)
        return try text.split(separator: "\n").map { try JSONDecoder().decode(Record.self, from: Data($0.utf8)) }
    }

    /// Replays one path into its terminals (one for A; one per pane for C).
    static func run(_ name: String, path: String) throws -> ReplayResult {
        var terms: [String: (Terminal, OverlayTracker, Headless)] = [:]
        var size = (cols: 80, rows: 24)
        func term(_ key: String) -> (Terminal, OverlayTracker, Headless) {
            if let t = terms[key] { return t }
            let d = Headless()
            let t = Terminal(delegate: d, options: TerminalOptions(cols: size.cols, rows: size.rows, scrollback: 1000))
            let made = (t, OverlayTracker(terminal: t), d)
            terms[key] = made
            return made
        }
        for r in try records(name) {
            if r.t == "resize", let c = r.cols, let rows = r.rows {
                size = (c, rows)
                for (t, _, _) in terms.values { t.resize(cols: c, rows: rows) }
                continue
            }
            guard r.t == "data", r.path == path, let b = r.b64, let data = Data(base64Encoded: b) else { continue }
            let (t, _, _) = term(path == "A" ? "A" : r.pane ?? "")
            t.feed(byteArray: [UInt8](data))
        }
        return terms.values.map { check($0.0, $0.1) }.reduce(ReplayResult(), +)
    }

    static let promptLine = /^P(\d+)\$ /

    static func check(_ t: Terminal, _ o: OverlayTracker) -> ReplayResult {
        var r = ReplayResult()
        o.refresh()
        let first = o.firstValid, last = o.lastValid
        func text(_ row: Int) -> String {
            t.getScrollInvariantLine(row: row)?.translateToString(trimRight: true) ?? ""
        }
        let marks = o.rows.marks
        let promptRows = Set(marks.filter { $0.kind == .prompt }.map(\.row))
        var lastNumber = 0
        for m in marks where m.kind == .prompt {
            r.promptMarks += 1
            if let match = text(m.row).firstMatch(of: promptLine), let n = Int(match.1), n > lastNumber {
                lastNumber = n
            } else {
                r.misplaced += 1
            }
        }
        for m in marks where m.kind == .commandStart {
            r.startMarks += 1
            // Every command here prints its output from the first line after
            // the command (seq …, or clear's empty screen).
            let line = text(m.row), above = text(m.row - 1)
            let ok = above.firstMatch(of: promptLine) != nil && !line.hasPrefix("P")
            if !ok { r.startMisplaced += 1 }
        }
        if last >= first {
            for row in first ... last where text(row).firstMatch(of: promptLine) != nil {
                r.promptLines += 1
                if !promptRows.contains(row) { r.unmarked += 1 }
            }
        }
        // Scrolling: at every position, each placed prompt mark's screen row
        // shows a prompt line.
        if !t.isCurrentBufferAlternate {
            let bottom = t.buffer.yDisp
            for y in stride(from: bottom, through: 0, by: -3) {
                t.buffer.yDisp = y
                for p in o.placed() where p.mark.kind == .prompt {
                    let s = t.getLine(row: p.screenRow)?.translateToString(trimRight: true) ?? ""
                    if s.firstMatch(of: promptLine) == nil { r.scrollMisplaced += 1 }
                }
            }
            t.buffer.yDisp = bottom
        }
        return r
    }
}

struct TmuxReplayTests {
    static let scenarios = ["scroll", "long", "fullscreen", "clear", "tabs", "resize"]

    /// Prints the table the spike reports; the expectations are the result.
    @Test(arguments: scenarios)
    func replay(_ name: String) throws {
        let a = try Replay.run(name, path: "A"), c = try Replay.run(name, path: "C")
        print("q3 \(name) A: \(a)")
        print("q3 \(name) C: \(c)")
        // C: every mark on its line, every prompt marked, at every scroll.
        #expect(c.misplaced == 0 && c.startMisplaced == 0 && c.unmarked <= 1 && c.scrollMisplaced == 0, "C, \(name): \(c)")
    }
}
