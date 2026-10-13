// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import PortenvTerm
import SwiftTerm
import SwiftUI

/// Command blocks, slice 0 (R-0170, R-0171): how folding and "the first and
/// last 20 lines" can work on a terminal grid. SPIKE ONLY, not for merge.
///
/// The board's session (git pull, a failing npm test, a long build, a
/// running claude) is played through the real MarkedTerminalView, with real
/// OSC 133 marks. Then it's drawn two ways:
/// - A, the grid decorated in place: main's overlay, rows never move;
/// - B, finished blocks as cards read back from the same terminal buffer
///   (scroll-invariant rows between the marks), with the live terminal only
///   for the block that's running.
@MainActor
enum BlocksSpike {
    static func render(to dir: URL) throws {
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        for (suffix, name) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            let appearance = NSAppearance(named: name)!
            NSApp.appearance = appearance
            let term = sessionTerminal(appearance: appearance)
            try snapshot(term, size: NSSize(width: 980, height: 640), to: dir.appendingPathComponent("A-grid-\(suffix).png"),
                         background: term.nativeBackgroundColor, appearance: appearance)
            let blocks = BlockReader.blocks(in: term)
            let palette = TerminalPalette.matching(appearance)
            let cards = NSHostingView(rootView: BlockCards(blocks: blocks, palette: palette)
                .frame(width: 980, height: 1500, alignment: .top)
                .background(Color(nsColor: .textBackgroundColor)))
            try snapshot(cards, size: NSSize(width: 980, height: 1500), to: dir.appendingPathComponent("B-cards-\(suffix).png"),
                         background: nil, appearance: appearance)
            let report = blocks.map { "\($0.command) · \($0.state) · \($0.lines.count) lines" }.joined(separator: "\n")
            try report.write(to: dir.appendingPathComponent("blocks-\(suffix).txt"), atomically: true, encoding: .utf8)
        }
    }

    // MARK: The session

    static let esc = "\u{1b}"
    static func prompt(_ cmd: String) -> String { "\(esc)]133;A\u{07}\(esc)[1m$\(esc)[0m \(esc)]133;B\u{07}\(cmd)\r\n\(esc)]133;C\u{07}" }
    static func end(_ code: Int) -> String { "\(esc)]133;D;\(code)\u{07}" }

    static var session: String {
        var s = prompt("git pull") + "Already up to date.\r\n" + end(0)
        s += prompt("npm test -- retries")
        s += "  \(esc)[31m×\(esc)[0m retries a declined payment once (412 ms)\r\n"
        s += "      Expected attempts: 2   Received: 3\r\n\r\n"
        s += "Tests: \(esc)[31m1 failed\(esc)[0m, \(esc)[32m23 passed\(esc)[0m, 24 total\r\n" + end(1)
        s += prompt("npm run build") + "vite v6.0.3 building for production…\r\n"
        for i in 1...2398 { s += "transforming (\(i)) src/module-\(i).ts\r\n" }
        s += "\(esc)[32m✓\(esc)[0m built in 40.71s\r\n" + end(0)
        s += prompt("claude --continue") + "● Ran the test 40 times: 8 failures, all on the second retry.\r\n"
        return s
    }

    static func sessionTerminal(appearance: NSAppearance) -> MarkedTerminalView {
        let view = TerminalViews.tab()
        view.appearance = appearance
        view.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        view.frame = NSRect(x: 0, y: 0, width: 980, height: 640)
        view.ingest(Array(session.utf8))
        return view
    }

    // MARK: Snapshot

    static func snapshot(_ view: NSView, size: NSSize, to file: URL, background: NSColor?, appearance: NSAppearance) throws {
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = appearance
        view.frame = NSRect(origin: .zero, size: size)
        window.contentView = view
        view.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { throw FirstRunError("no bitmap") }
        view.cacheDisplay(in: view.bounds, to: rep)
        var out = rep
        if let background, let under = view.bitmapImageRepForCachingDisplay(in: view.bounds),
           let ctx = NSGraphicsContext(bitmapImageRep: under) {
            NSGraphicsContext.saveGraphicsState()
            NSGraphicsContext.current = ctx
            background.setFill()
            view.bounds.fill()
            rep.draw(in: view.bounds, from: .zero, operation: .sourceOver, fraction: 1, respectFlipped: true, hints: nil)
            NSGraphicsContext.restoreGraphicsState()
            out = under
        }
        guard let png = out.representation(using: .png, properties: [:]) else { throw FirstRunError("no PNG") }
        try png.write(to: file)
    }
}

/// One block read back from the terminal's buffer.
struct SpikeBlock {
    enum State: CustomStringConvertible {
        case succeeded, failed(Int), running
        var description: String {
            switch self {
            case .succeeded: "succeeded"
            case let .failed(c): "failed \(c)"
            case .running: "running"
            }
        }
    }
    let command: String
    let state: State
    /// The output rows, as styled text, from the buffer.
    let lines: [AttributedString]
}

/// Reads blocks from a MarkedTerminalView's marks and buffer, with SwiftTerm's
/// public API only (scroll-invariant rows, cells and their attributes).
@MainActor
enum BlockReader {
    static func blocks(in view: MarkedTerminalView) -> [SpikeBlock] {
        let t = view.getTerminal()
        view.tracker.refresh()
        let marks = view.tracker.rows.marks.sorted { $0.row < $1.row }
        let palette = TerminalPalette.matching(view.effectiveAppearance)
        var out: [SpikeBlock] = []
        for (i, m) in marks.enumerated() where m.kind == .commandStart {
            let promptRow = marks[..<i].last { $0.kind == .prompt }?.row ?? m.row
            let promptText = t.getScrollInvariantLine(row: promptRow)?.translateToString(trimRight: true) ?? ""
            let command = promptText.components(separatedBy: "$ ").dropFirst().joined(separator: "$ ")
            let end = marks[(i + 1)...].first { if case .commandEnd = $0.kind { return true }; return $0.kind == .prompt }
            var state = SpikeBlock.State.running
            var last = view.tracker.cursorRow
            if let end, case let .commandEnd(exit) = end.kind {
                state = (exit ?? 0) == 0 ? .succeeded : .failed(exit ?? 0)
                last = end.row - 1
            }
            var lines: [AttributedString] = []
            if last >= m.row {
                for r in m.row...last {
                    guard let line = t.getScrollInvariantLine(row: r) else { continue }
                    lines.append(styled(line, cols: t.cols, palette: palette))
                }
            }
            while let l = lines.last, String(l.characters).trimmingCharacters(in: .whitespaces).isEmpty { lines.removeLast() }
            out.append(SpikeBlock(command: command, state: state, lines: lines))
        }
        return out
    }

    /// A buffer line as styled text: each cell's character and foreground.
    static func styled(_ line: BufferLine, cols: Int, palette: TerminalPalette) -> AttributedString {
        var s = AttributedString()
        var run = "", runColor: Attribute.Color = .defaultColor
        func flush() {
            guard !run.isEmpty else { return }
            var a = AttributedString(run)
            if case let .ansi256(code) = runColor, code < 16 {
                let c = palette.ansi[Int(code)]
                a.foregroundColor = Color(red: Double(c.red) / 255, green: Double(c.green) / 255, blue: Double(c.blue) / 255)
            }
            s += a
            run = ""
        }
        for col in 0..<min(cols, line.count) {
            let cell = line[col]
            let ch = cell.getCharacter()
            if cell.attribute.fg != runColor { flush(); runColor = cell.attribute.fg }
            run.append(ch == "\u{0}" ? " " : ch)
        }
        flush()
        while let lastChar = s.characters.last, lastChar == " " { s.characters.removeLast() }
        return s
    }
}

/// Option B: the board's cards, from the blocks read back.
struct BlockCards: View {
    let blocks: [SpikeBlock]
    let palette: TerminalPalette
    static let keep = 20

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { i, b in
                card(b, isLast: i == blocks.count - 1)
            }
        }
        .padding(14)
    }

    @ViewBuilder private func card(_ b: SpikeBlock, isLast: Bool) -> some View {
        let edge: SwiftUI.Color = switch b.state {
        case .failed: .red
        case .running: .accentColor
        case .succeeded: SwiftUI.Color.primary.opacity(0.12)
        }
        // A success that has scrolled off the latest screen folds to its header.
        let folded: Bool = if case .succeeded = b.state { !isLast && b.lines.count <= 2 * Self.keep } else { false }
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text("$ ").foregroundStyle(.secondary) + Text(b.command)
                Spacer()
                switch b.state {
                case .succeeded: Text("✓").foregroundStyle(.green)
                case let .failed(c): Text("✗ exit \(c)").foregroundStyle(.red)
                case .running: Text("● running").foregroundStyle(SwiftUI.Color.accentColor)
                }
            }
            .font(.system(size: 13, design: .monospaced))
            .padding(.horizontal, 12).padding(.vertical, 8)
            if !folded && !b.lines.isEmpty {
                Divider()
                VStack(alignment: .leading, spacing: 2) {
                    if b.lines.count > 2 * Self.keep {
                        ForEach(0..<Self.keep, id: \.self) { Text(b.lines[$0]) }
                        HStack(spacing: 6) {
                            Text("… \(b.lines.count - 2 * Self.keep) lines hidden ·").foregroundStyle(.secondary)
                            Text("Show All Output").foregroundStyle(SwiftUI.Color.accentColor)
                        }
                        ForEach(b.lines.count - Self.keep..<b.lines.count, id: \.self) { Text(b.lines[$0]) }
                    } else {
                        ForEach(0..<b.lines.count, id: \.self) { Text(b.lines[$0]) }
                    }
                }
                .font(.system(size: 13, design: .monospaced))
                .padding(.horizontal, 12).padding(.vertical, 8)
            }
        }
        .background(RoundedRectangle(cornerRadius: 8).fill(SwiftUI.Color.primary.opacity(0.04)))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(edge, lineWidth: 1))
    }
}
