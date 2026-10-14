// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// A tab view takes the same system colours as the main window's terminal,
/// in light and dark, and follows a change of appearance (GUIDELINES §4.5).
/// Set TAB_RENDERS to a folder for light and dark PNGs.
@MainActor
struct TabViewColoursTests {
    func host(_ name: NSAppearance.Name) -> (MarkedTerminalView, NSWindow) {
        _ = NSApplication.shared
        let v = TerminalViews.tab()
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 640, height: 300), styleMask: [.titled], backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        w.appearance = NSAppearance(named: name)
        w.contentView = v
        v.frame = w.contentLayoutRect
        return (v, w)
    }

    func expectColours(_ v: MarkedTerminalView, _ appearance: NSAppearance, _ label: String) {
        let system = TerminalPalette.systemColours(appearance)
        #expect(v.nativeBackgroundColor == NSColor(system.background), "\(label) background")
        #expect(v.nativeForegroundColor == NSColor(system.foreground), "\(label) foreground")
        #expect(v.caretColor == NSColor(system.caret), "\(label) caret")
    }

    @Test func lightAndDarkMatchTheSystem() throws {
        for name in [NSAppearance.Name.aqua, .darkAqua] {
            let (v, _) = host(name)
            v.viewDidChangeEffectiveAppearance()
            expectColours(v, v.effectiveAppearance, name.rawValue)
            #expect(TerminalPalette.isDark(v.effectiveAppearance) == (name == .darkAqua))
        }
    }

    /// Switching the window from light to dark recolours the view.
    @Test func followsAChangeOfAppearance() {
        let (v, w) = host(.aqua)
        v.viewDidChangeEffectiveAppearance()
        let light = v.nativeBackgroundColor
        w.appearance = NSAppearance(named: .darkAqua)
        v.viewDidChangeEffectiveAppearance()
        expectColours(v, v.effectiveAppearance, "after switching to dark")
        #expect(v.nativeBackgroundColor != light)
    }

    @Test func rendersInBothAppearances() throws {
        guard let dir = ProcessInfo.processInfo.environment["TAB_RENDERS"] else { return }
        for (suffix, name) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            let (v, _) = host(name)
            v.viewDidChangeEffectiveAppearance()
            let s = ScriptedTabSource(tab: "@1")
            v.attach(s)
            v.apply(.reset(TabTerminalSize(cols: 80, rows: 18), seed: Array((
                "\u{1b}]133;A\u{07}work@acme-api:~/acme-api$ \u{1b}]133;B\u{07}git status --short\r\n\u{1b}]133;C\u{07}"
                    + " \u{1b}[32mM\u{1b}[0m src/retry.ts\r\n \u{1b}[31mD\u{1b}[0m src/old.ts\r\n\u{1b}]133;D;0\u{07}"
                    + "\u{1b}]133;A\u{07}work@acme-api:~/acme-api$ \u{1b}]133;B\u{07}").utf8)))
            v.layoutSubtreeIfNeeded()
            let rep = try #require(v.bitmapImageRepForCachingDisplay(in: v.bounds))
            v.cacheDisplay(in: v.bounds, to: rep)
            // SwiftTerm paints its background on its layer, which a cached
            // display leaves out: drawn under it here, as ScreenRenders does.
            let under = try #require(v.bitmapImageRepForCachingDisplay(in: v.bounds))
            let context = try #require(NSGraphicsContext(bitmapImageRep: under))
            NSGraphicsContext.saveGraphicsState()
            NSGraphicsContext.current = context
            v.nativeBackgroundColor.setFill()
            v.bounds.fill()
            rep.draw(in: v.bounds, from: .zero, operation: .sourceOver, fraction: 1, respectFlipped: true, hints: nil)
            NSGraphicsContext.restoreGraphicsState()
            try under.representation(using: .png, properties: [:])?
                .write(to: URL(fileURLWithPath: dir).appendingPathComponent("tab-view-\(suffix).png"))
        }
    }
}
