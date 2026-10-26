// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// Spike question 3's prototype on a real SwiftTerm view: one tab's output
/// from tmux control mode (recorded from tmux 3.4, tests/spike/q3), with
/// the overlay drawn over it. Off screen; set Q3_RENDERS to a folder to get
/// light and dark PNGs of each position.
@MainActor
struct OverlayPrototypeTests {
    func view(cols: Int = 80, rows: Int = 24) -> (MarkedTerminalView, NSWindow) {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        v.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        v.getTerminal().resize(cols: cols, rows: rows)
        let size = v.getOptimalFrameSize().size
        let w = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.borderless], backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        w.contentView = v
        v.frame = NSRect(origin: .zero, size: size)
        return (v, w)
    }

    /// Feeds pane %0's control-mode output, applying the recorded resizes
    /// to the view's frame as the app's window would.
    func replay(_ name: String, into v: MarkedTerminalView) throws {
        for r in try Replay.records(name) {
            if r.t == "resize", let c = r.cols, let rows = r.rows {
                v.getTerminal().resize(cols: c, rows: rows)
                v.frame = NSRect(origin: .zero, size: v.getOptimalFrameSize().size)
                v.window?.setContentSize(v.frame.size)
                continue
            }
            if r.t == "data", r.path == "C", r.pane == "%0", let b = r.b64, let d = Data(base64Encoded: b) {
                v.ingest([UInt8](d))
            }
        }
    }

    /// Draws the view and its overlay into a bitmap (the window is never
    /// on screen, so AppKit wouldn't draw it otherwise), after letting
    /// SwiftTerm place its caret, which it does on a frame change.
    @discardableResult
    func draw(_ v: MarkedTerminalView) -> NSBitmapImageRep? {
        v.setFrameSize(v.frame.size)
        v.layoutSubtreeIfNeeded()
        guard let rep = v.bitmapImageRepForCachingDisplay(in: v.bounds) else { return nil }
        v.cacheDisplay(in: v.bounds, to: rep)
        return rep
    }

    func text(_ v: MarkedTerminalView, row: Int) -> String {
        v.getTerminal().getLine(row: row)?.translateToString(trimRight: true) ?? ""
    }

    func render(_ v: MarkedTerminalView, _ name: String) throws {
        guard let dir = ProcessInfo.processInfo.environment["Q3_RENDERS"] else { return }
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            v.window?.appearance = NSAppearance(named: appearance)
            v.needsDisplay = true
            guard let rep = draw(v) else { continue }
            try rep.representation(using: .png, properties: [:])?
                .write(to: URL(fileURLWithPath: dir).appendingPathComponent("q3-\(name)-\(suffix).png"))
        }
    }

    /// At the bottom and scrolled up through the scrollback, every band and
    /// every status is drawn on a prompt row (a status on its own command's,
    /// finding 23); and the rows are where SwiftTerm draws its own caret.
    @Test(arguments: ["scroll", "long", "fullscreen", "clear", "resize"])
    func theOverlayFollowsTheRows(_ name: String) throws {
        let (v, _) = view()
        try replay(name, into: v)
        let t = v.getTerminal()

        // SwiftTerm's own geometry: its caret sits on the cursor's row, which
        // is the last prompt's row.
        draw(v)
        let cursorRow = t.getCursorLocation().y
        let caret = v.subviews.first { String(describing: type(of: $0)) == "CaretView" }
        #expect(caret != nil)
        if let caret {
            #expect(abs(caret.frame.minY - v.rect(screenRow: cursorRow).minY) < 1, "caret at \(caret.frame), row \(v.rect(screenRow: cursorRow))")
        }
        let last = try #require(v.overlay.drawn.last { $0.kind == .prompt })
        #expect(abs(last.rect.minY - v.rect(screenRow: cursorRow).minY) < 0.5)

        try render(v, "\(name)-bottom")
        var positions = 0
        let bottom = t.buffer.yDisp
        for up in stride(from: 0, through: 60, by: 6) {
            v.scrollTo(row: max(0, bottom - up))
            draw(v)
            positions += 1
            for d in v.overlay.drawn {
                let row = Int(((v.bounds.height - d.rect.maxY) / v.rowHeight).rounded())
                let s = text(v, row: row)
                #expect(s.hasPrefix("P"), "\(name), \(up) up: \(d.kind) on \"\(s)\"")
            }
            if up == 12 { try render(v, "\(name)-up12") }
        }
        #expect(positions > 1)
    }

    /// Finding 23: bash prints a command's D;<exit> when the next prompt
    /// starts, on that prompt's row. Each status must sit on the prompt of
    /// the command that produced it, not on the next one.
    ///
    /// The bytes are bash 5.2 with Portenv's integration in the toolbox
    /// image, as recorded through control mode in
    /// tests/spike/q3/fixtures/scroll.jsonl (pane %0, path C): the same
    /// prompt, echo, bracketed-paste, PS0 (\u{01} C \u{02}) and D-then-A
    /// framing, byte for byte, without the spike's tmux-wrapped duplicate
    /// of each mark. Only the commands, their output and exit codes are the
    /// owner's sequence: a failing git apply (128), cd (no output), a
    /// failing git merge (128), then git config.
    static let recorded: String = {
        let e = "\u{1b}"
        func prompt(_ n: Int) -> String { "\(e)]133;A\u{07}\(e)[?2004hP\(n)# \(e)]133;B\u{07}" }
        func run(_ command: String) -> String { "\(command)\r\n\(e)[?2004l\r\u{01}\(e)]133;C\u{07}\u{02}" }
        func end(_ code: Int) -> String { "\(e)]133;D;\(code)\u{07}" }
        return prompt(1)
            + run("git apply fix.patch") + "error: corrupt patch at line 7\r\n" + end(128) + prompt(2)
            + run("cd acme-api") + end(0) + prompt(3)
            + run("git merge origin/main") + "fatal: refusing to merge unrelated histories\r\n" + end(128) + prompt(4)
            + run("git config user.name") + "Example Person\r\n" + end(0) + prompt(5)
    }()

    @Test func eachStatusSitsOnThePromptOfItsOwnCommand() throws {
        let (v, _) = view()
        v.ingest(Array(Self.recorded.utf8))
        draw(v)
        var statuses: [String: Int?] = [:]
        for d in v.overlay.drawn {
            guard case let .commandEnd(exit) = d.kind else { continue }
            let row = Int(((v.bounds.height - d.rect.maxY) / v.rowHeight).rounded())
            statuses[text(v, row: row)] = exit
        }
        #expect(statuses == [
            "P1# git apply fix.patch": 128,
            "P2# cd acme-api": 0,
            "P3# git merge origin/main": 128,
            "P4# git config user.name": 0,
        ])
        // The blocks agree: each command ends with its own code.
        let blocks = BlockReader(view: v).blocks()
        #expect(blocks.map(\.command) == ["git apply fix.patch", "cd acme-api", "git merge origin/main", "git config user.name"])
        #expect(blocks.map(\.state) == [.failed(exit: 128), .succeeded, .failed(exit: 128), .succeeded])
    }
}
