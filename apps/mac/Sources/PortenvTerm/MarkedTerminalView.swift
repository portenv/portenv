// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm

/// One tab's terminal view (ADR 0020; spike question 3, ADR 0017): a
/// SwiftTerm terminal view with an overlay pinned to the rows its OSC 133
/// marks are on. Fed one tab's own output through a TabTerminalSource, so
/// the marks arrive with the text they belong to. Command blocks draw on its
/// overlay; split view puts one in each pane.
///
/// The overlay draws what a block header needs to line up with: a tint band
/// on each prompt row, a rule above it, and each finished command's status
/// at the right of its own prompt row (not of the row its end mark is on,
/// which is the next prompt's; finding 23). It's redrawn whenever the view
/// scrolls, receives output or changes size.
public final class MarkedTerminalView: TerminalView {
    public private(set) var tracker: OverlayTracker!
    public let overlay = MarkOverlay()
    /// Where an answer card or other card sits over the text (blocks place
    /// it with `rect(screenRow:)`). Clicks reach its cards and nothing else.
    public let cards = CardLayer()
    public private(set) var source: (any TabTerminalSource)?
    /// Called after each `reset` is drawn (the stream started, or
    /// reconnected), with the tab's size.
    public var onReset: (TabTerminalSize) -> Void = { _ in }
    /// Called when the stream ends. On `.tabClosed` the last screen stays.
    public var onEnded: (TabTerminalEnd) -> Void = { _ in }
    /// The size this view's frame fits, as SwiftTerm last worked it out.
    public internal(set) var fitted: TabTerminalSize?
    /// Opens a ⌘-clicked link as TerminalLinks decides (R-0161).
    public let links = TerminalLinkOpener()
    /// The filter in front of the terminal (ADR 0020, decision 9).
    private var filter = TerminalFilter()
    private var reading: Task<Void, Never>?
    private let sizes = SizeRelay()
    /// Called if anything replaced the view's delegate, which then gets put
    /// back (R-0161). A failure in debug builds; tests record it.
    nonisolated(unsafe) static var delegateReplaced: () -> Void = { assertionFailure("a tab view's terminalDelegate was replaced") }

    /// Lines of scrollback a tab keeps (ADR 0020, decision 9).
    public static let scrollbackLines = 10_000

    /// The text's size in points (R-0362): SF Mono, 13 pt by default
    /// (GUIDELINES.md §4.5). A new size reflows the grid, and the new size in
    /// cells reaches the tab through `sizeChanged`, the same path a window
    /// resize takes, so the shell and its programs redraw for it.
    ///
    /// SwiftTerm's font setter resizes through its public `resize`, which
    /// also soft-resets the terminal (cursor keys, scroll region, colours: a
    /// full-screen program would lose its modes). So the font changes while
    /// the view is briefly zero-sized, where SwiftTerm doesn't resize, and
    /// the frame coming back does it, exactly as a window resize would.
    public var textSize: CGFloat = TerminalTextSize.defaultPoints {
        didSet {
            guard textSize != oldValue else { return }
            let size = frame.size
            setFrameSize(.zero)
            font = Self.font(textSize)
            setFrameSize(size)
        }
    }

    /// Called with ⌘= or ⌘⇧= (or ⌘+) while this terminal has the keys:
    /// View › Bigger's other shortcuts (R-0362), which its menu item can't
    /// also carry.
    public var onTextSizeKey: (TerminalTextSize.Step) -> Void = { _ in }

    static func font(_ points: CGFloat) -> NSFont {
        NSFont.monospacedSystemFont(ofSize: points, weight: .regular)
    }

    public override init(frame: NSRect) {
        super.init(frame: frame)
        tracker = OverlayTracker(terminal: getTerminal())
        overlay.host = self
        overlay.frame = bounds
        overlay.autoresizingMask = [.width, .height]
        addSubview(overlay)
        cards.frame = bounds
        cards.autoresizingMask = [.width, .height]
        addSubview(cards)
        sizes.view = self
        terminalDelegate = sizes
        changeScrollback(Self.scrollbackLines)
        // SwiftTerm starts at the system's monospaced 13 pt, the default; set
        // it only if that ever differs (the setter resizes the grid).
        if font != Self.font(textSize) { font = Self.font(textSize) }
        TerminalColours.apply(to: self, for: effectiveAppearance)
    }

    /// Bigger's ⌘= and ⌘⇧= (R-0362), only in the terminal that has the keys
    /// (every view in the window is asked). ⌘+ and ⌘− are the menu's; every
    /// other key goes on to SwiftTerm and the program as before.
    public override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if window?.firstResponder === self,
           TerminalTextSize.step(charactersIgnoringModifiers: event.charactersIgnoringModifiers,
                                 modifiers: event.modifierFlags) == .bigger {
            onTextSizeKey(.bigger)
            return true
        }
        return super.performKeyEquivalent(with: event)
    }

    /// Follows light and dark with the same colours as the main window's
    /// terminal (TerminalColours), so every pane matches.
    public override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        TerminalColours.apply(to: self, for: effectiveAppearance)
    }

    /// The delegate is the view's own relay, always: SwiftTerm hands it the
    /// clipboard, links and titles, so nothing else may stand there
    /// (R-0161). SwiftTerm's property can't be overridden, so it's checked
    /// before every byte from the workspace is fed, which is when a program
    /// could reach those, and put back if it was changed.
    func keepDelegate() {
        if terminalDelegate !== sizes {
            Self.delegateReplaced()
            terminalDelegate = sizes
        }
    }

    /// Pasted text is cleaned before it's sent (ADR 0020, R-0133 item 3),
    /// then bracketed when the program asked for it.
    public override func paste(_: Any) {
        paste(text: NSPasteboard.general.string(forType: .string) ?? "")
    }

    /// Text dropped on the view, or sent by a Service, would be another way
    /// in: SwiftTerm 1.20.0 accepts neither (it registers no dragged types
    /// and answers no Services), and the view says so explicitly, so any
    /// text that isn't typed comes through `paste(text:)` (R-0167).
    public override func validRequestor(forSendType _: NSPasteboard.PasteboardType?, returnType _: NSPasteboard.PasteboardType?) -> Any? {
        nil
    }

    public override func registerForDraggedTypes(_: [NSPasteboard.PasteboardType]) {}

    /// Sends `text` as a paste, cleaned.
    public func paste(text: String) {
        let clean = PasteText.clean(text)
        guard !clean.isEmpty else { return }
        let bracketed = getTerminal().bracketedPasteMode
        if bracketed { send(txt: "\u{1b}[200~") }
        send(txt: clean)
        if bracketed { send(txt: "\u{1b}[201~") }
    }

    /// Shows a tab: drops the current one (it keeps running), then plays
    /// the new source's events.
    public func attach(_ source: any TabTerminalSource) {
        detach()
        self.source = source
        landing = true
        lastLanding = nil
        let t = getTerminal()
        let size = fitted ?? TabTerminalSize(cols: t.cols, rows: t.rows)
        fitted = size
        source.resize(size)
        reading = Task { @MainActor [weak self] in
            for await event in source.events {
                guard let self else { return }
                self.apply(event)
            }
        }
    }

    /// Stops showing the tab; the tab keeps running.
    public func detach() {
        remember()
        reading?.cancel()
        reading = nil
        source?.close()
        source = nil
    }

    func apply(_ event: TabTerminalEvent) {
        let t = getTerminal()
        switch event {
        case let .reset(size, seed):
            t.resetToInitialState()
            filter = TerminalFilter()
            tracker.reset()
            if size.cols != t.cols || size.rows != t.rows { t.resize(cols: size.cols, rows: size.rows) }
            tracker.seeding = true
            ingest(seed)
            tracker.seeding = false
            if landing {
                landing = false
                land()
            }
            onReset(size)
            // The tab was at another size (another viewer, or this view
            // changed while disconnected): ask for this view's own.
            if let fitted, fitted != size { source?.resize(fitted) }
        case let .output(bytes):
            ingest(bytes)
        case let .resized(size):
            if size.cols != t.cols || size.rows != t.rows { t.resize(cols: size.cols, rows: size.rows) }
            overlay.needsDisplay = true
        case let .ended(why):
            onEnded(why)
        }
    }


    @available(*, unavailable)
    required init?(coder _: NSCoder) { fatalError("not used") }

    /// One tab's output, as tmux control mode gives it, through the
    /// default-deny filter.
    public func ingest(_ bytes: [UInt8]) {
        keepDelegate()
        if !tracker.seeding, scrolledUp { outputWhileScrolledUp = true }
        let allowed = filter.filter(bytes)
        if !allowed.isEmpty {
            feeding = true
            feed(byteArray: allowed[...])
            feeding = false
        }
        overlay.needsDisplay = true
    }

    /// The height of one row, in points: SwiftTerm draws `rows` rows in its
    /// optimal frame.
    public var rowHeight: CGFloat {
        getOptimalFrameSize().height / CGFloat(max(1, getTerminal().rows))
    }

    /// A screen row's rectangle in this view (not flipped: row 0 is at the
    /// top), as SwiftTerm draws it.
    public func rect(screenRow: Int) -> NSRect {
        let h = rowHeight
        return NSRect(x: 0, y: bounds.height - h * CGFloat(screenRow + 1), width: bounds.width, height: h)
    }

    /// The emulator's own replies (to DA, DSR, DECRQSS, DECRQM, XTWINOPS,
    /// XTGETTCAP, XTVERSION, OSC 4/10/11 and OSC 52 queries) never reach the
    /// tab (ADR 0020, R-0133 item 1). tmux emulates the pane and answers them
    /// itself, and a reply sent from here would let a program make the
    /// terminal type into the tab, as the person. Only what the person types,
    /// pastes, clicks or enters through IME goes to the source, through
    /// SwiftTerm's `send(data:)`.
    public override func send(source _: Terminal, data _: ArraySlice<UInt8>) {}

    // MARK: Landing on a switch (GUIDELINES §4.1)

    /// What a tab the owner left was showing: where they had scrolled up
    /// to, the screen then, and whether output had come since they chose
    /// that place. Only for tabs left scrolled up.
    struct Kept: Equatable {
        var linesFromBottom: Int
        var screen: [String]
        var outputSince: Bool
    }

    /// By tab, for the tabs this pane has shown.
    private var kept: [String: Kept] = [:]
    /// The next reset is a switch to the source just attached.
    private var landing = false
    /// The landing for the tab on screen, nil until its seed is drawn
    /// (tests).
    private(set) var lastLanding: TerminalLanding?
    /// Output came while the owner was scrolled up in the tab on screen,
    /// since they last scrolled.
    private(set) var outputWhileScrolledUp = false

    /// Whether the owner has scrolled up from the prompt.
    var scrolledUp: Bool { canScroll && scrollPosition < 1 }

    /// The sidebar's selection changed but this tab stayed on screen: the
    /// same rule as a switch, for the tab as it is.
    public func shownBySelection() {
        guard !landing, source != nil else { return }
        let l = TerminalLanding.on(switched: true, scrolledUp: scrolledUp, newOutputSince: outputWhileScrolledUp)
        lastLanding = l
        if l == .bottom { toBottom() }
    }

    /// The view scrolled by the owner's hand (the wheel, the scroller, or
    /// this view's own landing; output scrolls another way): the place they
    /// read from is theirs again, whatever came before.
    func viewScrolled() {
        if !feeding { outputWhileScrolledUp = false }
    }

    /// Output is being drawn: its scrolling isn't the owner's.
    private var feeding = false

    /// Records the tab being left.
    private func remember() {
        guard let tab = source?.tab else { return }
        defer { outputWhileScrolledUp = false }
        guard scrolledUp else {
            kept[tab] = nil
            return
        }
        let place = getTerminal().buffer.yDisp
        let outputSince = outputWhileScrolledUp
        toBottom()
        let bottom = getTerminal().buffer.yDisp
        let screen = screenLines()
        scrollTo(row: place)
        kept[tab] = Kept(linesFromBottom: bottom - place, screen: screen, outputSince: outputSince)
    }

    /// After a switch's seed: the prompt, or the owner's place in it.
    private func land() {
        guard let tab = source?.tab else { return }
        let k = kept.removeValue(forKey: tab)
        toBottom()
        let l = TerminalLanding.on(switched: true, scrolledUp: k != nil,
                                   newOutputSince: k.map { $0.outputSince || $0.screen != screenLines() } ?? false)
        lastLanding = l
        if l == .keep, let k {
            scrollTo(row: max(0, getTerminal().buffer.yDisp - k.linesFromBottom))
        }
        outputWhileScrolledUp = false
    }

    private func toBottom() { scrollTo(row: Int.max) }

    /// The screen's text, at the bottom.
    private func screenLines() -> [String] {
        let t = getTerminal()
        return (0 ..< t.rows).map { t.getLine(row: $0)?.translateToString(trimRight: true) ?? "" }
    }

    public override func scrolled(source terminal: Terminal, yDisp: Int) {
        super.scrolled(source: terminal, yDisp: yDisp)
        overlay.needsDisplay = true
    }

    public override func bufferActivated(source: Terminal) {
        super.bufferActivated(source: source)
        overlay.needsDisplay = true
    }

    public override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        overlay.needsDisplay = true
    }
}

/// Passes what the terminal emulator produces to the view's source: its
/// size in cells, and keystrokes, pastes and mouse reports. A program in the
/// box can't read or set the Mac's clipboard (OSC 52), open links or set a
/// title through it (ADR 0020, decision 9). SwiftTerm calls it on the main
/// thread; staying synchronous keeps keystrokes in order.
@MainActor
private final class SizeRelay: TerminalViewDelegate {
    weak var view: MarkedTerminalView?

    nonisolated func sizeChanged(source _: TerminalView, newCols: Int, newRows: Int) {
        MainActor.assumeIsolated {
            let size = TabTerminalSize(cols: newCols, rows: newRows)
            view?.fitted = size
            view?.source?.resize(size)
        }
    }

    nonisolated func send(source _: TerminalView, data: ArraySlice<UInt8>) {
        let bytes = Array(data)
        MainActor.assumeIsolated { view?.source?.send(bytes) }
    }

    // The delegate check runs in every path SwiftTerm hands the delegate
    // something (R-0167), as well as before each byte is fed.
    nonisolated func setTerminalTitle(source _: TerminalView, title _: String) {
        MainActor.assumeIsolated { view?.keepDelegate() }
    }
    nonisolated func hostCurrentDirectoryUpdate(source _: TerminalView, directory _: String?) {}
    nonisolated func scrolled(source _: TerminalView, position _: Double) {
        MainActor.assumeIsolated { view?.viewScrolled() }
    }
    /// A ⌘-clicked link goes through TerminalLinks, as in the attached
    /// terminal (R-0161).
    nonisolated func requestOpenLink(source _: TerminalView, link: String, params _: [String: String]) {
        MainActor.assumeIsolated {
            guard let view else { return }
            view.keepDelegate()
            view.links.request(link, in: view)
        }
    }
    nonisolated func bell(source _: TerminalView) {}
    nonisolated func clipboardCopy(source _: TerminalView, content _: Data) {
        MainActor.assumeIsolated { view?.keepDelegate() }
    }
    nonisolated func clipboardRead(source _: TerminalView) -> Data? { nil }
    nonisolated func iTermContent(source _: TerminalView, content _: ArraySlice<UInt8>) {}
    nonisolated func rangeChanged(source _: TerminalView, startY _: Int, endY _: Int) {}
}

/// Holds cards over the terminal (the inline answer card). Only its cards
/// take clicks; everywhere else, clicks go to the terminal.
public final class CardLayer: NSView {
    public override func hitTest(_ point: NSPoint) -> NSView? {
        let hit = super.hitTest(point)
        return hit === self ? nil : hit
    }
}

/// The marks, drawn over the terminal's text. It never takes a click.
public final class MarkOverlay: NSView {
    weak var host: MarkedTerminalView?
    /// The rectangles drawn last, by kind, for tests and renders.
    public private(set) var drawn: [(kind: OverlayRows.Kind, rect: NSRect)] = []

    public override func hitTest(_: NSPoint) -> NSView? { nil }

    public override func draw(_: NSRect) {
        guard let host else { return }
        drawn = []
        for placed in host.tracker.placed() {
            let row = host.rect(screenRow: placed.screenRow)
            switch placed.mark.kind {
            case .prompt:
                NSColor.controlAccentColor.withAlphaComponent(0.12).setFill()
                row.fill()
                NSColor.controlAccentColor.withAlphaComponent(0.6).setFill()
                NSRect(x: row.minX, y: row.maxY - 1, width: row.width, height: 1).fill()
            case .commandEnd, .commandStart:
                // A status is drawn on its command's prompt row, below.
                continue
            }
            drawn.append((placed.mark.kind, row))
        }
        // Each command's status at the right of its own prompt row, not of
        // the row its end mark is on, which is the next prompt's (the
        // shell sends D when that prompt starts; finding 23).
        for status in host.tracker.statuses() {
            let row = host.rect(screenRow: status.row)
            let ok = status.exit == 0
            let label = ok ? "✓" : "✗ exit \(status.exit.map(String.init) ?? "?")"
            let attrs: [NSAttributedString.Key: Any] = [
                .font: NSFont.monospacedSystemFont(ofSize: max(9, row.height * 0.7), weight: .semibold),
                .foregroundColor: ok ? NSColor.systemGreen : NSColor.systemRed,
            ]
            let size = (label as NSString).size(withAttributes: attrs)
            (label as NSString).draw(at: NSPoint(x: row.maxX - size.width - 8, y: row.minY + (row.height - size.height) / 2),
                                     withAttributes: attrs)
            drawn.append((.commandEnd(exit: status.exit), row))
        }
    }
}
