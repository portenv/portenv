// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftTerm
import SwiftUI
import Testing

@testable import PortenvKit

/// R-0075, R-0076: with the inspector open, narrowing the window from
/// 1000 pt to 800 or 640 pt hid the prompt, leaving only the caret at the
/// top left. The terminal side had its own 640 pt minimum, so it was laid
/// out wider than what the inspector left, centred and cut on both sides.
/// Measured in a real window with a real terminal: after narrowing, the
/// columns on screen still show the prompt's line from its first column.
@MainActor
@Suite(.serialized)
struct NarrowWindowTerminalTests {
    static let prompt = "work@tabs-demo:~$"

    /// The terminal the test reads back after laying out.
    @MainActor final class Probe {
        var terminal: TerminalView?
    }

    private struct Terminal: NSViewRepresentable {
        let probe: Probe
        func makeNSView(context _: Context) -> TerminalView {
            let view = TerminalView(frame: .zero)
            view.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
            view.feed(text: NarrowWindowTerminalTests.prompt + " ")
            probe.terminal = view
            return view
        }

        func updateNSView(_: TerminalView, context _: Context) {}
    }

    /// The main window's shape: the tab bar over the terminal, the
    /// inspector open beside them, and the window's own minimum (AppRoot).
    private struct Window: View {
        let probe: Probe
        @State var open = true
        var body: some View {
            VStack(spacing: 0) {
                Color.clear.frame(height: 30)
                Terminal(probe: probe)
            }
            .boxWindowColumns(inspectorOpen: $open) { Text("Inspector") }
            .frame(minWidth: 640, minHeight: 440)
        }
    }

    /// What the terminal shows on screen on the cursor's line: the columns
    /// whose cells are wholly in sight, at each window width in turn.
    private func shownLines(widths: [CGFloat]) throws -> [(width: CGFloat, shown: String, buffer: String)] {
        _ = NSApplication.shared
        let probe = Probe()
        let window = NSWindow(contentViewController: NSHostingController(rootView: Window(probe: probe)))
        window.styleMask.insert(.fullSizeContentView)
        window.orderFront(nil)
        defer { window.close() }
        var out: [(CGFloat, String, String)] = []
        for width in widths {
            window.setContentSize(NSSize(width: width, height: 600))
            for _ in 0..<30 { RunLoop.main.run(until: Date().addingTimeInterval(0.03)) }
            let view = try #require(probe.terminal)
            let terminal = view.getTerminal()
            let font = view.font
            let cell = font.advancement(forGlyph: font.glyph(withName: "W")).width
            let seen = view.visibleRect
            let first = Int((seen.minX / cell).rounded(.up))
            let last = min(terminal.cols, Int((seen.maxX / cell).rounded(.down)))
            let line = try #require(terminal.getLine(row: terminal.getCursorLocation().y))
            let text = line.translateToString(trimRight: true)
            let chars = Array(text)
            let shown = first < last ? String(chars[min(first, chars.count)..<min(last, chars.count)]) : ""
            out.append((window.frame.width, shown, text))
        }
        return out
    }

    @Test func narrowingKeepsThePromptInSight() throws {
        let lines = try shownLines(widths: [1000, 800, 640, 1000])
        for line in lines {
            #expect(line.buffer.hasPrefix(Self.prompt), "the terminal itself lost the prompt at \(line.width) pt")
            #expect(line.shown.hasPrefix(Self.prompt), "at \(line.width) pt the screen shows \"\(line.shown)\"")
        }
    }
}
