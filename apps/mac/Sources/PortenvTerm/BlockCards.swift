// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm
import SwiftUI

/// Reads a tab's command blocks and their rows back from its terminal
/// (slice 0's option B, R-0174): only from the buffer, which holds what the
/// default-deny filter let through, after the emulator drew it. So a card
/// shows exactly the text the terminal shows, as plain styled text: no
/// sequence a program wrote reaches it, and a card can't open a link, set a
/// title or touch the clipboard. SwiftTerm's public API only.
@MainActor
public final class BlockReader {
    private let view: MarkedTerminalView
    private var terminal: Terminal { view.getTerminal() }

    public init(view: MarkedTerminalView) { self.view = view }

    /// The tab's blocks, oldest first. None while a full-screen program
    /// shows its own screen (its marks belong to the main screen).
    public func blocks() -> [CommandBlock] {
        guard !terminal.isCurrentBufferAlternate else { return [] }
        let tracker = view.tracker!
        tracker.refresh()
        let marks = tracker.rows.marks
        // Trimmed marks keep the start of a block that outgrew the
        // scrollback (its command is gone with its prompt line).
        return CommandBlocks.blocks(marks: tracker.rows.trimmed + marks, cursorRow: tracker.cursorRow) { [self] promptRow in
            command(promptRow: promptRow, marks: marks)
        }
    }

    /// The command typed on a prompt: from its B mark's column to the end
    /// of the prompt, across wrapped rows up to the command's start.
    func command(promptRow: Int, marks: [OverlayRows.Mark]) -> String? {
        guard let first = terminal.getScrollInvariantLine(row: promptRow) else { return nil }
        let col = view.tracker.inputColumn[ObjectIdentifier(first)] ?? 0
        let start = marks.filter { $0.kind == .commandStart && $0.row > promptRow }.map(\.row).min() ?? promptRow + 1
        var text = first.translateToString(trimRight: true, startCol: col)
        for r in (promptRow + 1) ..< max(promptRow + 1, start) {
            guard let line = terminal.getScrollInvariantLine(row: r) else { break }
            text += line.translateToString(trimRight: true)
        }
        return text.trimmingCharacters(in: .whitespaces)
    }

    /// Rows already styled, by line identity: a card redraws without
    /// reading the buffer again. A line that changes (the running block's
    /// last rows) isn't cached.
    private var cache: [ObjectIdentifier: (palette: String, text: AttributedString)] = [:]
    private var colours: [Attribute.Color: SwiftUI.Color] = [:]

    /// One row as styled text, or nil if it left the scrollback.
    public func row(_ r: Int, palette: TerminalPalette) -> AttributedString? {
        guard let line = terminal.getScrollInvariantLine(row: r) else { return nil }
        let key = ObjectIdentifier(line)
        let settled = r < view.tracker.cursorRow
        if settled, let hit = cache[key], hit.palette == palette.name { return hit.text }
        if colours.isEmpty || cache.first?.value.palette != palette.name { colours.removeAll() }
        let text = styled(line, cols: terminal.cols, palette: palette)
        if settled {
            if cache.count > 4096 { cache.removeAll() }
            cache[key] = (palette.name, text)
        }
        return text
    }

    /// A buffer line as styled text: its characters, as SwiftTerm puts them
    /// together, then each run's foreground colour (the 16 ANSI colours from
    /// the app's palette, the 256-colour cube, true colour), bold, italic and
    /// underline. Nothing else.
    func styled(_ line: BufferLine, cols: Int, palette: TerminalPalette) -> AttributedString {
        let plain = line.translateToString(trimRight: true, skipNullCellsFollowingWide: true)
        var text = AttributedString(plain)
        let count = plain.count
        guard count > 0 else { return text }
        // Runs of cells with one look, in characters (a wide character's
        // second cell adds none).
        var runs: [(start: Int, length: Int, attr: Attribute)] = []
        var at = 0
        for col in 0 ..< min(cols, line.count) where at < count {
            let cell = line[col]
            if cell.width == 0 { continue }
            let a = cell.attribute
            if let last = runs.last, last.attr == a { runs[runs.count - 1].length += 1 } else { runs.append((at, 1, a)) }
            at += 1
        }
        for run in runs where run.attr.fg != .defaultColor || !run.attr.style.isEmpty {
            let lo = text.index(text.startIndex, offsetByCharacters: run.start)
            let hi = text.index(lo, offsetByCharacters: min(run.length, count - run.start))
            if let c = colour(run.attr.fg, palette: palette) { text[lo ..< hi].foregroundColor = c }
            var intent: InlinePresentationIntent = []
            if run.attr.style.contains(.bold) { intent.insert(.stronglyEmphasized) }
            if run.attr.style.contains(.italic) { intent.insert(.emphasized) }
            if !intent.isEmpty { text[lo ..< hi].inlinePresentationIntent = intent }
            if run.attr.style.contains(.underline) { text[lo ..< hi].underlineStyle = .single }
        }
        return text
    }

    private func colour(_ c: Attribute.Color, palette: TerminalPalette) -> SwiftUI.Color? {
        if let hit = colours[c] { return hit }
        let made = Self.color(c, palette: palette)
        if let made { colours[c] = made }
        return made
    }

    static func color(_ c: Attribute.Color, palette: TerminalPalette) -> SwiftUI.Color? {
        func rgb(_ r: UInt8, _ g: UInt8, _ b: UInt8) -> SwiftUI.Color {
            SwiftUI.Color(red: Double(r) / 255, green: Double(g) / 255, blue: Double(b) / 255)
        }
        switch c {
        case .defaultColor, .defaultInvertedColor:
            return nil
        case let .trueColor(r, g, b):
            return rgb(r, g, b)
        case let .ansi256(code):
            if code < 16 { let p = palette.ansi[Int(code)]; return rgb(p.red, p.green, p.blue) }
            if code >= 232 { let v = UInt8(8 + Int(code - 232) * 10); return rgb(v, v, v) }
            let i = Int(code) - 16
            let steps: [UInt8] = [0, 95, 135, 175, 215, 255]
            return rgb(steps[i / 36], steps[(i / 6) % 6], steps[i % 6])
        }
    }
}

/// The finished blocks as the board's cards (R-0139, R-0174). Folded
/// blocks show their header; open ones their rows, a long one its first and
/// last 20 until Show All Output. A click on the header folds or opens it.
public struct BlockCards: View {
    let blocks: [CommandBlock]
    let reader: BlockReader
    let palette: TerminalPalette
    let now: Date
    /// Blocks the person opened or folded (by id); the rest follow
    /// `BlockLayout.openByDefault`.
    @State private var toggled: Set<Int> = []
    @State private var showAll: Set<Int> = []

    public init(blocks: [CommandBlock], reader: BlockReader, palette: TerminalPalette, now: Date = Date()) {
        self.blocks = blocks
        self.reader = reader
        self.palette = palette
        self.now = now
    }

    /// For renders and tests: blocks to show open or with every row.
    public init(blocks: [CommandBlock], reader: BlockReader, palette: TerminalPalette, now: Date, toggled: Set<Int>, showAll: Set<Int> = []) {
        self.init(blocks: blocks, reader: reader, palette: palette, now: now)
        _toggled = State(initialValue: toggled)
        _showAll = State(initialValue: showAll)
    }

    public var body: some View {
        LazyVStack(alignment: .leading, spacing: 10) {
            ForEach(blocks) { block in
                BlockCard(block: block, reader: reader, palette: palette, now: now,
                          open: BlockLayout.openByDefault(block.state) != toggled.contains(block.id),
                          showAll: showAll.contains(block.id),
                          toggle: { toggled.formSymmetricDifference([block.id]) },
                          expand: { showAll.insert(block.id) })
            }
        }
        .padding(14)
    }
}

struct BlockCard: View {
    let block: CommandBlock
    let reader: BlockReader
    let palette: TerminalPalette
    let now: Date
    let open: Bool
    let showAll: Bool
    let toggle: () -> Void
    let expand: () -> Void

    private var edge: SwiftUI.Color {
        switch block.state {
        case .failed: SwiftUI.Color(nsColor: .systemRed)
        case .running: SwiftUI.Color.accentColor
        case .succeeded: SwiftUI.Color.primary.opacity(0.12)
        }
    }

    var body: some View {
        let shown = BlockLayout.shown(block, open: open, showAll: showAll)
        VStack(alignment: .leading, spacing: 0) {
            header
            if shown != .folded {
                Divider()
                LazyVStack(alignment: .leading, spacing: 2) {
                    switch shown {
                    case .folded: EmptyView()
                    case let .all(rows): rowsView(rows)
                    case let .headTail(head, hidden, tail):
                        rowsView(head)
                        HStack(spacing: 6) {
                            Text(BlockText.hidden(hidden) + " \u{00B7}").foregroundStyle(.secondary)
                            Button(BlockText.showAllOutput, action: expand).buttonStyle(.link)
                        }
                        rowsView(tail)
                    }
                }
                .font(.system(size: 12, design: .monospaced))
                .textSelection(.enabled)
                .padding(.horizontal, 12).padding(.vertical, 8)
            }
        }
        .background(RoundedRectangle(cornerRadius: 8).fill(SwiftUI.Color.primary.opacity(0.035)))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(edge, lineWidth: 1))
    }

    private var header: some View {
        Button(action: toggle) {
            HStack(spacing: 8) {
                (Text("$ ").foregroundStyle(.secondary) + Text(block.command))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 12)
                status
            }
            .font(.system(size: 12, design: .monospaced))
            .padding(.horizontal, 12).padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var status: some View {
        let words = BlockText.status(block, now: now)
        switch block.state {
        case .succeeded:
            (Text("\u{2713} ").foregroundStyle(SwiftUI.Color(nsColor: .systemGreen)) + Text(words).foregroundStyle(.secondary))
        case .failed:
            (Text("\u{2717} ").foregroundStyle(SwiftUI.Color(nsColor: .systemRed))
                + Text(words).foregroundStyle(SwiftUI.Color(nsColor: .systemRed)))
        case .running:
            (Text("\u{25CF} ").foregroundStyle(SwiftUI.Color.accentColor) + Text(words).foregroundStyle(SwiftUI.Color.accentColor))
        }
    }

    private func rowsView(_ rows: ClosedRange<Int>) -> some View {
        ForEach(Array(rows), id: \.self) { r in
            Text(reader.row(r, palette: palette) ?? AttributedString(" "))
        }
    }
}
