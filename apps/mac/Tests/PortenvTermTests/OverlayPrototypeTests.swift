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

    /// At the bottom and scrolled up through the scrollback, every band is
    /// drawn on a prompt row and every status on the row after a command's
    /// output; and the rows are where SwiftTerm draws its own caret.
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
            for d in v.overlay.drawn where d.kind == .prompt {
                let row = Int(((v.bounds.height - d.rect.maxY) / v.rowHeight).rounded())
                let s = text(v, row: row)
                #expect(s.hasPrefix("P"), "\(name), \(up) up: a band on \"\(s)\"")
            }
            if up == 12 { try render(v, "\(name)-up12") }
        }
        #expect(positions > 1)
    }
}
