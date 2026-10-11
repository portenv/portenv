// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import PortenvKit

/// R-0073: in a window 641 pt wide the inspector's lines were cut mid-word
/// with no ellipsis ("Add a server to move th", "Saved when th"). The
/// terminal side's own 640 pt minimum pushed the inspector column half out
/// of the window. Measured in a real window: at every width the column is
/// whole, at least its minimum wide, and each line ends inside it.
@MainActor
@Suite(.serialized)
struct NarrowWindowInspectorTests {
    /// The inspector's lines the review found cut, and a long one.
    static let lines = [
        InspectorText.moveUnavailable(targets: []) ?? "",
        InspectorText.kind("release", machine: "studio"),
        InspectorText.encryption("Test server"),
        InspectorText.moveNote,
    ]

    /// The views laid out, read back in window coordinates.
    @MainActor final class Probe {
        var column: NSView?
        var lines: [Int: NSView] = [:]
    }

    private struct Mark: NSViewRepresentable {
        let found: (NSView) -> Void
        func makeNSView(context _: Context) -> NSView {
            let view = NSView()
            found(view)
            return view
        }

        func updateNSView(_: NSView, context _: Context) {}
    }

    /// The main window's shape with the inspector open: its lines in a
    /// scroll view, as InspectorView lays them out (16 pt each side).
    private struct Window: View {
        let probe: Probe
        @State var open = true
        var body: some View {
            Color.clear
                .boxWindowColumns(inspectorOpen: $open) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 8) {
                            ForEach(Array(NarrowWindowInspectorTests.lines.enumerated()), id: \.offset) { i, line in
                                Text(line).font(.system(size: 12))
                                    .background(Mark { probe.lines[i] = $0 })
                            }
                        }
                        .padding(.horizontal, 16)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .background(Mark { probe.column = $0 })
                }
                .frame(minWidth: 640, minHeight: 440)
        }
    }

    private func frame(_ view: NSView) -> CGRect { view.convert(view.bounds, to: nil) }

    @Test(arguments: [CGFloat(1000), 800, 641, 640])
    func theInspectorFitsItsColumn(_ width: CGFloat) throws {
        _ = NSApplication.shared
        let probe = Probe()
        let window = NSWindow(contentViewController: NSHostingController(rootView: Window(probe: probe)))
        window.styleMask.insert(.fullSizeContentView)
        window.setContentSize(NSSize(width: width, height: 600))
        window.orderFront(nil)
        defer { window.close() }
        for _ in 0..<30 { RunLoop.main.run(until: Date().addingTimeInterval(0.03)) }
        let inside = try #require(window.contentView).bounds
        let view = try #require(probe.column)
        let column = frame(view)
        // The whole column is on screen, from its minimum width up.
        #expect(column.width >= BoxWindowColumns<EmptyView>.inspectorWidth.min - 0.5, "column \(column.width) pt")
        #expect(column.maxX <= inside.maxX + 0.5, "column ends at \(column.maxX) in \(inside.width) pt")
        #expect(abs(view.visibleRect.width - view.bounds.width) < 0.5, "\(view.visibleRect.width) of \(view.bounds.width) pt in sight")
        // Each line wraps inside the column, never past its edge.
        for i in Self.lines.indices {
            let line = frame(try #require(probe.lines[i]))
            #expect(line.minX >= column.minX && line.maxX <= column.maxX - 15.5,
                    "\"\(Self.lines[i])\" spans \(line.minX)–\(line.maxX), column \(column.minX)–\(column.maxX)")
            #expect(line.maxX <= inside.maxX, "\"\(Self.lines[i])\" ends at \(line.maxX) in \(inside.width) pt")
        }
    }
}
