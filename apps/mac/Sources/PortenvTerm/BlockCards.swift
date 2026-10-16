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

    /// One row's plain text, for Find.
    public func text(_ r: Int) -> String? {
        terminal.getScrollInvariantLine(row: r)?.translateToString(trimRight: true, skipNullCellsFollowingWide: true)
    }

    /// The first row still in the scrollback.
    public var firstRow: Int { view.tracker.firstValid }

    /// How many of a block's output lines are past the scrollback.
    public func trimmedLines(of block: CommandBlock) -> Int {
        guard let rows = block.output else { return 0 }
        return max(0, min(firstRow, rows.upperBound + 1) - rows.lowerBound)
    }

    /// The block with only the output still in the scrollback, so a card
    /// lays out (first and last 20, lines hidden) what it can show.
    public func kept(_ block: CommandBlock) -> CommandBlock {
        guard let rows = block.output, rows.lowerBound < firstRow else { return block }
        let output = rows.upperBound >= firstRow ? firstRow ... rows.upperBound : nil
        return CommandBlock(id: block.id, command: block.command, state: block.state, output: output,
                            startedAt: block.startedAt, endedAt: block.endedAt)
    }

    /// Copy Output's text: the whole block (R-0171), read in chunks with a
    /// yield between them like Find, so a long block never holds the main
    /// thread; first, when it outgrew the scrollback, a line saying so. Nil
    /// if cancelled.
    public func outputText(of block: CommandBlock) async -> String? {
        var lines: [String] = []
        if let rows = block.output, rows.upperBound >= firstRow {
            var lo = max(rows.lowerBound, firstRow)
            while lo <= rows.upperBound {
                let hi = min(lo + BlockFind.chunkRows - 1, rows.upperBound)
                for r in lo ... hi { lines.append(text(r) ?? "") }
                lo = hi + 1
                await Task.yield()
                if Task.isCancelled { return nil }
            }
        }
        return BlockCopy.output(lines: lines, trimmed: trimmedLines(of: block))
    }

    /// Whether Run Again can type into the tab: it's attached to one.
    public var canRunAgain: Bool { view.source != nil }

    /// Run Again: types the command and Return into the tab, as the person,
    /// through the same path as their keystrokes. Asking first is the
    /// caller's job (`BlockCopy.asksBeforeRunningAgain`).
    public func runAgain(_ block: CommandBlock) {
        guard let bytes = BlockCopy.runAgainInput(block.command) else { return }
        view.source?.send(bytes)
    }

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

/// The blocks as the board's cards (R-0139, R-0174, R-0207), in one list:
/// each block a section whose header stays at the top while its output
/// scrolls under it, with "↑ Top of block". ⌘↑ and ⌘↓ move between blocks;
/// ⌘F finds across every block's command and output, folded or hidden rows
/// too, and opens what a match is in.
public struct BlocksView: View {
    let blocks: [CommandBlock]
    let reader: BlockReader
    let palette: TerminalPalette
    let now: Date

    /// Blocks the person opened or folded (by id); the rest follow
    /// `BlockLayout.openByDefault`.
    @State private var toggled: Set<Int>
    @State private var showAll: Set<Int>
    @State private var selected: Int?
    @State private var finding: Bool
    @State private var query: String
    @State private var found: [BlockFind.Match] = []
    @State private var current: Int?
    @State private var searching: Task<Void, Never>?
    @State private var copying: Task<Void, Never>?
    /// The block Run Again is asking about.
    @State private var asking: CommandBlock?
    /// Where each block sits in the list's visible area, for the header that
    /// stays at the top.
    /// Open blocks whose top has scrolled above the list while their
    /// output still shows: their header stays at the top. Only a change in
    /// this set redraws the list, not every scrolled point (R-0243).
    @State private var underTop: Set<Int> = []
    /// Where VoiceOver's cursor is: a click on a header moves it there
    /// (R-0244).
    @AccessibilityFocusState private var spoken: Int?
    @FocusState private var findFocused: Bool
    /// Renders and tests: an output row to start scrolled to.
    private let startAt: Int?

    public init(blocks: [CommandBlock], reader: BlockReader, palette: TerminalPalette, now: Date = Date(),
                toggled: Set<Int> = [], showAll: Set<Int> = [], selected: Int? = nil, query: String? = nil, startAt: Int? = nil) {
        self.startAt = startAt
        self.blocks = blocks
        self.reader = reader
        self.palette = palette
        self.now = now
        _toggled = State(initialValue: toggled)
        _showAll = State(initialValue: showAll)
        _selected = State(initialValue: selected)
        _finding = State(initialValue: query != nil)
        _query = State(initialValue: query ?? "")
        if let query {
            let m = BlockFind.matches(query, in: blocks) { reader.text($0) }
            _found = State(initialValue: m)
            _current = State(initialValue: m.isEmpty ? nil : 0)
        }
    }

    func isOpen(_ b: CommandBlock) -> Bool { BlockLayout.openByDefault(b.state) != toggled.contains(b.id) }

    public var body: some View {
        VStack(spacing: 0) {
            if finding { findBar }
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 10) {
                        ForEach(blocks) { block in
                            VStack(alignment: .leading, spacing: 0) {
                                header(block, proxy: proxy, pinned: false)
                                    .id(Self.headID(block.id))
                                if block.state == .running {
                                    Rectangle().fill(SwiftUI.Color.accentColor).frame(height: 2).accessibilityHidden(true)
                                }
                                BlockBody(block: block, reader: reader, palette: palette, open: isOpen(block),
                                          showAll: showAll.contains(block.id), highlights: highlights(block.id),
                                          expand: { showAll.insert(block.id) })
                            }
                            .background(RoundedRectangle(cornerRadius: 8).fill(SwiftUI.Color.primary.opacity(0.035)))
                            .overlay(RoundedRectangle(cornerRadius: 8)
                                .stroke(edge(block.state, selected: selected == block.id), lineWidth: selected == block.id ? 2 : 1))
                            .contextMenu { menu(block, proxy: proxy) }
                            .id(block.id)
                            .onGeometryChange(for: Bool.self) { g in
                                let f = g.frame(in: .named(Self.space))
                                return f.minY < -1 && f.maxY > 80
                            } action: { under in
                                if under { underTop.insert(block.id) } else { underTop.remove(block.id) }
                            }
                        }
                    }
                    .padding(14)
                }
                .coordinateSpace(.named(Self.space))
                // The header of the block whose output is scrolling under the
                // top stays there, with "↑ Top of block" (the board's sticky
                // header).
                .overlay(alignment: .top) {
                    if let block = blocks.first(where: pinned) {
                        header(block, proxy: proxy, pinned: true)
                            .background(RoundedRectangle(cornerRadius: 8).fill(SwiftUI.Color.primary.opacity(0.035)))
                            .background(RoundedRectangle(cornerRadius: 8).fill(.background))
                            .overlay(RoundedRectangle(cornerRadius: 8).stroke(edge(block.state, selected: selected == block.id), lineWidth: 1))
                            .shadow(color: .black.opacity(0.15), radius: 3, y: 1)
                            .padding(.horizontal, 14).padding(.top, 4)
                    }
                }
                .onAppear { if let startAt { proxy.scrollTo(BlockBody.rowID(startAt), anchor: .top) } }
                .onChange(of: selected) { _, id in if let id { withAnimation { proxy.scrollTo(id, anchor: .top) } } }
                .onChange(of: current) { _, _ in reveal(proxy) }
            }
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(phases: .down) { press in key(press) }
        .alert(asking.map { BlockCopy.runAgainQuestion(command: $0.command, author: nil).title } ?? "",
               isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }), presenting: asking) { block in
            Button(BlockCopy.runAgain) { reader.runAgain(block) }
            Button("Cancel", role: .cancel) {}
        } message: { block in
            Text(BlockCopy.runAgainQuestion(command: block.command, author: nil).message)
        }
    }

    static let space = "blocks"
    static func headID(_ id: Int) -> String { "head-\(id)" }

    /// "↑ Top of block": to the block's header, not animated, and again once
    /// the lazy rows above it have their real heights, so a jump over
    /// thousands of rows doesn't land short (R-0239).
    func top(_ block: CommandBlock, _ proxy: ScrollViewProxy) {
        proxy.scrollTo(Self.headID(block.id), anchor: .top)
        DispatchQueue.main.async { proxy.scrollTo(Self.headID(block.id), anchor: .top) }
    }

    func header(_ block: CommandBlock, proxy: ScrollViewProxy, pinned: Bool) -> some View {
        BlockHeader(block: block, now: now, open: isOpen(block), pinned: pinned, selected: selected == block.id,
                    highlights: highlights(block.id)[-1] ?? [],
                    actions: actions(block), spoken: pinned ? nil : $spoken,
                    toggle: {
                        toggled.formSymmetricDifference([block.id])
                        if !pinned { spoken = block.id }
                    },
                    top: { top(block, proxy) })
    }

    // MARK: Actions

    func actions(_ block: CommandBlock) -> BlockActions {
        BlockActions(copyOutput: block.output == nil ? nil : { copyOutput(block) },
                     copyCommand: block.command.isEmpty ? nil : { Self.copy(block.command) },
                     runAgain: reader.canRunAgain && BlockCopy.runAgainInput(block.command) != nil ? { runAgain(block) } : nil)
    }

    /// The context menu: the hover actions, then Copy Both, Collapse and
    /// Jump to Start (GUIDELINES 4.2).
    @ViewBuilder func menu(_ block: CommandBlock, proxy: ScrollViewProxy) -> some View {
        let a = actions(block)
        Button(BlockCopy.copyOutput) { a.copyOutput?() }.disabled(a.copyOutput == nil)
        Button(BlockCopy.copyCommand) { a.copyCommand?() }.disabled(a.copyCommand == nil)
        Button(BlockCopy.copyBoth) { copyOutput(block, withCommand: true) }.disabled(block.command.isEmpty && block.output == nil)
        Divider()
        Button(BlockCopy.runAgain + "\u{2026}") { a.runAgain?() }.disabled(a.runAgain == nil)
        Divider()
        Button(isOpen(block) ? BlockCopy.collapse : BlockCopy.expand) { toggled.formSymmetricDifference([block.id]) }
        Button(BlockCopy.jumpToStart) { withAnimation { proxy.scrollTo(block.id, anchor: .top) } }
    }

    /// Copies the whole block's output (R-0171), read in chunks off the
    /// buffer; a second copy cancels one still reading.
    func copyOutput(_ block: CommandBlock, withCommand: Bool = false) {
        copying?.cancel()
        let reader = reader
        copying = Task { @MainActor in
            guard let text = await reader.outputText(of: block) else { return }
            Self.copy(withCommand ? BlockCopy.both(command: block.command, output: text) : text)
        }
    }

    /// Run Again asks first unless portenvd recorded that you ran it; until
    /// blocks carry their author (slice 4), that's every time (R-0139).
    func runAgain(_ block: CommandBlock) {
        if BlockCopy.asksBeforeRunningAgain(authorIsYou: nil) { asking = block } else { reader.runAgain(block) }
    }

    static func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    /// A header stays at the top while its block's output scrolls under it:
    /// the block's top has gone above the list's top, and its bottom hasn't.
    func pinned(_ b: CommandBlock) -> Bool { isOpen(b) && underTop.contains(b.id) }

    // MARK: Keys

    func key(_ press: KeyPress) -> KeyPress.Result {
        let cmd = press.modifiers.contains(.command)
        switch press.key {
        case .upArrow where cmd:
            selected = BlockNavigation.previous(from: selected, in: blocks)
            return .handled
        case .downArrow where cmd:
            selected = BlockNavigation.next(from: selected, in: blocks)
            return .handled
        case KeyEquivalent("c") where cmd && press.modifiers.contains(.option):
            // ⌥⌘C copies the selected block's output.
            guard let id = selected, let block = blocks.first(where: { $0.id == id }), block.output != nil else { return .ignored }
            copyOutput(block)
            return .handled
        case KeyEquivalent("f") where cmd:
            finding = true
            findFocused = true
            return .handled
        case KeyEquivalent("g") where cmd:
            step(press.modifiers.contains(.shift) ? -1 : 1)
            return .handled
        case .escape where finding:
            closeFind()
            return .handled
        default:
            return .ignored
        }
    }

    // MARK: Find

    private var findBar: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass").foregroundStyle(.secondary).accessibilityHidden(true)
            TextField("Find", text: $query)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 260)
                .focused($findFocused)
                .onSubmit { step(1) }
                .onChange(of: query) { _, q in search(q) }
            Text(query.isEmpty ? "" : BlockFind.count(current: current, total: found.count))
                .foregroundStyle(.secondary)
                .monospacedDigit()
            Button { step(-1) } label: { Image(systemName: "chevron.up") }
                .disabled(found.isEmpty).help("Find Previous (⇧⌘G)").accessibilityLabel("Find Previous")
            Button { step(1) } label: { Image(systemName: "chevron.down") }
                .disabled(found.isEmpty).help("Find Next (⌘G)").accessibilityLabel("Find Next")
            Spacer()
            Button("Done", action: closeFind).keyboardShortcut(.cancelAction)
        }
        .controlSize(.small)
        .padding(.horizontal, 14).padding(.vertical, 6)
        .background(.bar)
        .overlay(alignment: .bottom) { Divider() }
    }

    /// Searches 150 ms after the last keystroke, in chunks of rows with a
    /// yield between them, so a long scrollback never holds the main thread
    /// (R-0174); a new keystroke cancels the search under way.
    func search(_ q: String) {
        searching?.cancel()
        guard !q.isEmpty else { found = []; current = nil; return }
        let blocks = blocks, reader = reader
        searching = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(150))
            var out: [BlockFind.Match] = []
            for b in blocks {
                if Task.isCancelled { return }
                out += BlockFind.matches(q, inCommandOf: b)
                guard let rows = b.output else { continue }
                var lo = rows.lowerBound
                while lo <= rows.upperBound {
                    let hi = min(lo + BlockFind.chunkRows - 1, rows.upperBound)
                    out += BlockFind.matches(q, block: b.id, rows: lo ... hi) { reader.text($0) }
                    lo = hi + 1
                    await Task.yield()
                    if Task.isCancelled { return }
                }
            }
            found = out
            current = out.isEmpty ? nil : 0
        }
    }

    func step(_ by: Int) {
        guard !found.isEmpty else { return }
        current = ((current ?? -by) + by + found.count) % found.count
    }

    func closeFind() {
        searching?.cancel()
        finding = false
        query = ""
        found = []
        current = nil
    }

    /// Shows the current match: opens its block, and every row when the
    /// match is in the hidden middle of a long one, then scrolls to it.
    func reveal(_ proxy: ScrollViewProxy) {
        guard let current, found.indices.contains(current) else { return }
        let m = found[current]
        guard let block = blocks.first(where: { $0.id == m.block }) else { return }
        if let row = m.row {
            if !isOpen(block) { toggled.formSymmetricDifference([block.id]) }
            if case let .headTail(head, _, tail) = BlockLayout.shown(block, open: true, showAll: false),
               !head.contains(row), !tail.contains(row) { showAll.insert(block.id) }
            withAnimation { proxy.scrollTo(BlockBody.rowID(row), anchor: .center) }
        } else {
            withAnimation { proxy.scrollTo(block.id, anchor: .top) }
        }
    }

    /// A block's matches by row (-1 for its command), and which one is
    /// current.
    func highlights(_ block: Int) -> [Int: [(range: Range<Int>, current: Bool)]] {
        var out: [Int: [(range: Range<Int>, current: Bool)]] = [:]
        for (i, m) in found.enumerated() where m.block == block {
            out[m.row ?? -1, default: []].append((m.range, i == current))
        }
        return out
    }
}

/// Marks find matches on a line: yellow, the current one orange.
func highlighted(_ text: AttributedString, _ marks: [(range: Range<Int>, current: Bool)]) -> AttributedString {
    var t = text
    let count = t.characters.count
    for m in marks where m.range.lowerBound < count {
        let lo = t.index(t.startIndex, offsetByCharacters: m.range.lowerBound)
        let hi = t.index(t.startIndex, offsetByCharacters: min(m.range.upperBound, count))
        t[lo ..< hi].backgroundColor = m.current ? SwiftUI.Color.orange.opacity(0.6) : SwiftUI.Color.yellow.opacity(0.45)
    }
    return t
}

/// A block's header: the disclosure chevron (always on a folded block, on
/// hover on an open one), the command, its status, and "↑ Top of block"
/// while it stays at the top. A button: a click, Space or Return folds or
/// opens it, and VoiceOver reads "npm run build, succeeded, 41 seconds" with
/// its collapsed or expanded state (R-0207).
struct BlockHeader: View {
    let block: CommandBlock
    let now: Date
    let open: Bool
    let pinned: Bool
    let selected: Bool
    let highlights: [(range: Range<Int>, current: Bool)]
    let actions: BlockActions
    /// The list's VoiceOver focus, for the header in the list (not the
    /// sticky copy): a click moves VoiceOver's cursor to it.
    let spoken: AccessibilityFocusState<Int?>.Binding?
    let toggle: () -> Void
    let top: () -> Void
    @State private var hovering = false

    var body: some View {
        HStack(spacing: 8) {
            Button(action: toggle) {
                HStack(spacing: 6) {
                    Image(systemName: open ? "chevron.down" : "chevron.right")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(.secondary)
                        .frame(width: 10)
                        .opacity(!open || hovering ? 1 : 0)
                    (Text("$ ").foregroundStyle(.secondary) + Text(highlighted(AttributedString(block.command), highlights)))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer(minLength: 12)
                    status
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(BlockText.spoken(block, now: now))
            .accessibilityValue(BlockText.expandedState(open: open))
            .onKeyPress(.return) { toggle(); return .handled }
            .modifier(SpokenFocus(spoken: spoken, id: block.id))
            .accessibilityActions {
                if let a = actions.copyOutput { Button(BlockCopy.copyOutput, action: a) }
                if let a = actions.copyCommand { Button(BlockCopy.copyCommand, action: a) }
                if let a = actions.runAgain { Button(BlockCopy.runAgain, action: a) }
            }
            if hovering || selected { toolbar }
            if pinned {
                // Set 12 pt apart from the status's time (R-0219): the row's
                // 8 pt and 4 more.
                Button(action: top) { Text("\u{2191} Top of block") }
                    .buttonStyle(.link)
                    .padding(.leading, 4)
                    .accessibilityLabel("Top of block")
            }
        }
        .font(.system(size: 12, design: .monospaced))
        .padding(.horizontal, 12).padding(.vertical, 8)
        .onHover { hovering = $0 }
    }

    /// The hover toolbar (the board's actions): shown on hover and on the
    /// selected block.
    private var toolbar: some View {
        HStack(spacing: 6) {
            if let a = actions.copyOutput { ActionButton(title: BlockCopy.copyOutput, action: a) }
            if let a = actions.copyCommand { ActionButton(title: BlockCopy.copyCommand, action: a) }
            if let a = actions.runAgain { ActionButton(title: BlockCopy.runAgain, action: a) }
        }
        .padding(.leading, 2)
        .accessibilityHidden(true)
    }

    /// The status, with "since 14:37" as its tooltip (R-0207): on the
    /// status itself, where the pointer rests, not on the whole button.
    private var status: some View {
        statusText.help(BlockText.since(block) ?? "")
    }

    @ViewBuilder private var statusText: some View {
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
}

/// Moves VoiceOver's cursor to a header when it's clicked (R-0244); the
/// sticky copy of a header has no binding and keeps the cursor where it is.
struct SpokenFocus: ViewModifier {
    let spoken: AccessibilityFocusState<Int?>.Binding?
    let id: Int

    func body(content: Content) -> some View {
        if let spoken { content.accessibilityFocused(spoken, equals: id) } else { content }
    }
}

/// What a block's header offers; nil when an action can't run (no output
/// yet, no command, or no tab to type into).
struct BlockActions {
    let copyOutput: (() -> Void)?
    let copyCommand: (() -> Void)?
    let runAgain: (() -> Void)?
}

/// A hover-toolbar button: 11 pt system text on a quiet fill, as on the
/// board.
struct ActionButton: View {
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 11))
                .padding(.horizontal, 8)
                .frame(minHeight: 22)
                .background(RoundedRectangle(cornerRadius: 5).fill(SwiftUI.Color.primary.opacity(0.1)))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

/// A block's output: nothing when folded; its rows, or its first and last
/// 20 with Show All Output; find matches marked.
struct BlockBody: View {
    let block: CommandBlock
    let reader: BlockReader
    let palette: TerminalPalette
    let open: Bool
    let showAll: Bool
    let highlights: [Int: [(range: Range<Int>, current: Bool)]]
    let expand: () -> Void

    static func rowID(_ r: Int) -> String { "row-\(r)" }

    var body: some View {
        let shown = BlockLayout.shown(reader.kept(block), open: open, showAll: showAll)
        Group {
            if shown != .folded {
                if block.state != .running { Divider() }
                // Lazy: Show All Output on a 2,400-line block builds only the
                // rows on screen, for the window and for VoiceOver (R-0239,
                // R-0243).
                LazyVStack(alignment: .leading, spacing: 2) {
                    // Rows past the scrollback show as one note, not blanks.
                    if trimmed > 0 {
                        Text(BlockCopy.trimmedNote(trimmed))
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                            .padding(.bottom, 4)
                    }
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
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private var trimmed: Int { reader.trimmedLines(of: block) }

    private func rowsView(_ rows: ClosedRange<Int>) -> some View {
        ForEach(Array(rows), id: \.self) { r in
            Text(highlighted(reader.row(r, palette: palette) ?? AttributedString(" "), highlights[r] ?? []))
                .id(Self.rowID(r))
        }
    }
}

func edge(_ state: CommandBlock.State, selected: Bool) -> SwiftUI.Color {
    if selected { return .accentColor }
    switch state {
    case .failed: return SwiftUI.Color(nsColor: .systemRed)
    // Running is a blue line under its header, not an outline: the accent
    // outline means selected (R-0239).
    case .running, .succeeded: return SwiftUI.Color.primary.opacity(0.12)
    }
}
