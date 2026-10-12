// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm

/// Spike question 3's prototype (ADR 0017): a SwiftTerm terminal view with
/// an overlay pinned to the rows its OSC 133 marks are on. Fed one tab's own
/// output (tmux control mode's %output for the pane), so the marks arrive
/// with the text they belong to.
///
/// The overlay draws what a block header needs to line up with: a tint band
/// on each prompt row, a rule above it, and the previous command's status at
/// the right of the row its end mark is on. It's redrawn whenever the view
/// scrolls, receives output or changes size.
public final class MarkedTerminalView: TerminalView {
    public private(set) var tracker: OverlayTracker!
    public let overlay = MarkOverlay()

    public override init(frame: NSRect) {
        super.init(frame: frame)
        tracker = OverlayTracker(terminal: getTerminal())
        overlay.host = self
        overlay.frame = bounds
        overlay.autoresizingMask = [.width, .height]
        addSubview(overlay)
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
