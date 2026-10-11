// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import PortenvKit

/// R-0069: the inspector toggle sits at the toolbar's trailing edge
/// (GUIDELINES §4: its last item), above the inspector column when it's
/// open, and doesn't move when the inspector opens or closes. Measured in a
/// real window with the main window's toolbar shape (title items at the
/// leading edge, the inspector at 260–400 pt).
@MainActor
@Suite(.serialized)
struct InspectorToggleToolbarTests {
    private struct Window: View {
        @State var open: Bool
        var body: some View {
            Color.clear.frame(minWidth: 640, minHeight: 400)
                .inspector(isPresented: $open) {
                    Text("Inspector")
                        .inspectorColumnWidth(min: 260, ideal: 300, max: 400)
                        .inspectorToggle(isOpen: $open)
                }
                .toolbar(removing: .title)
                .toolbar { ToolbarItem(placement: .navigation) { Text("acme-api") } }
        }
    }

    /// The toggle's frame in window coordinates, and how many toolbar items
    /// carry its words.
    private func toggle(open: Bool, appearance: NSAppearance.Name) -> (frame: CGRect?, items: Int, width: CGFloat) {
        _ = NSApplication.shared
        let window = NSWindow(contentViewController: NSHostingController(rootView: Window(open: open)))
        window.appearance = NSAppearance(named: appearance)
        window.styleMask.insert(.fullSizeContentView)
        window.setContentSize(NSSize(width: 1000, height: 600))
        window.orderFront(nil)
        defer { window.close() }
        for _ in 0..<40 { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        let words = open ? A11y.hideInspector : A11y.showInspector
        let items = (window.toolbar?.items ?? []).filter { $0.label == words }
        let frame = items.first?.view.map { $0.convert($0.bounds, to: nil) }
        return (frame, items.count, window.frame.width)
    }

    @Test(arguments: [NSAppearance.Name.aqua, .darkAqua])
    func theToggleSitsAtTheTrailingEdgeOpenOrClosed(_ appearance: NSAppearance.Name) throws {
        let open = toggle(open: true, appearance: appearance)
        let closed = toggle(open: false, appearance: appearance)
        for t in [open, closed] {
            #expect(t.items == 1)
            let frame = try #require(t.frame)
            #expect(frame.maxX >= t.width - 60, "maxX \(frame.maxX) of \(t.width)")
        }
        // Above the inspector column (at most 400 pt wide) when it's open,
        // and in the same place both ways.
        let o = try #require(open.frame), c = try #require(closed.frame)
        #expect(o.minX >= open.width - 400)
        #expect(abs(o.minX - c.minX) < 1)
    }
}
