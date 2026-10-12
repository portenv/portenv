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
/// on each prompt row, a rule above it, and the previous command's status at
/// the right of the row its end mark is on. It's redrawn whenever the view
/// scrolls, receives output or changes size.
public final class MarkedTerminalView: TerminalView {
    public private(set) var tracker: OverlayTracker!
    public let overlay = MarkOverlay()
    public private(set) var source: (any TabTerminalSource)?
    /// Called when the source ends.
    public var onEnded: (TabTerminalEnd) -> Void = { _ in }
    private var reading: Task<Void, Never>?
    private let sizes = SizeRelay()

    public override init(frame: NSRect) {
        super.init(frame: frame)
        tracker = OverlayTracker(terminal: getTerminal())
        overlay.host = self
        overlay.frame = bounds
        overlay.autoresizingMask = [.width, .height]
        addSubview(overlay)
        sizes.view = self
        terminalDelegate = sizes
    }

    /// Shows a tab: drops the current one (it keeps running), then plays
    /// the new source's events.
    public func attach(_ source: any TabTerminalSource) {
        detach()
        self.source = source
        let t = getTerminal()
        source.resize(TabTerminalSize(cols: t.cols, rows: t.rows))
        reading = Task { @MainActor [weak self] in
            for await event in source.events {
                guard let self else { return }
                self.apply(event)
            }
        }
    }

    /// Stops showing the tab; the tab keeps running.
    public func detach() {
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
            tracker.reset()
            if size.cols != t.cols || size.rows != t.rows { t.resize(cols: size.cols, rows: size.rows) }
            ingest(seed)
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

    /// One tab's output, as tmux control mode gives it.
    public func ingest(_ bytes: [UInt8]) {
        feed(byteArray: bytes[...])
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
        MainActor.assumeIsolated { view?.source?.resize(TabTerminalSize(cols: newCols, rows: newRows)) }
    }

    nonisolated func send(source _: TerminalView, data: ArraySlice<UInt8>) {
        let bytes = Array(data)
        MainActor.assumeIsolated { view?.source?.send(bytes) }
    }

    nonisolated func setTerminalTitle(source _: TerminalView, title _: String) {}
    nonisolated func hostCurrentDirectoryUpdate(source _: TerminalView, directory _: String?) {}
    nonisolated func scrolled(source _: TerminalView, position _: Double) {}
    nonisolated func requestOpenLink(source _: TerminalView, link _: String, params _: [String: String]) {}
    nonisolated func bell(source _: TerminalView) {}
    nonisolated func clipboardCopy(source _: TerminalView, content _: Data) {}
    nonisolated func clipboardRead(source _: TerminalView) -> Data? { nil }
    nonisolated func iTermContent(source _: TerminalView, content _: ArraySlice<UInt8>) {}
    nonisolated func rangeChanged(source _: TerminalView, startY _: Int, endY _: Int) {}
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
            case let .commandEnd(exit):
                let ok = exit == 0
                let label = ok ? "✓" : "✗ exit \(exit.map(String.init) ?? "?")"
                let attrs: [NSAttributedString.Key: Any] = [
                    .font: NSFont.monospacedSystemFont(ofSize: max(9, row.height * 0.7), weight: .semibold),
                    .foregroundColor: ok ? NSColor.systemGreen : NSColor.systemRed,
                ]
                let size = (label as NSString).size(withAttributes: attrs)
                (label as NSString).draw(at: NSPoint(x: row.maxX - size.width - 8, y: row.minY + (row.height - size.height) / 2),
                                         withAttributes: attrs)
            case .commandStart:
                break
            }
            drawn.append((placed.mark.kind, row))
        }
    }
}
