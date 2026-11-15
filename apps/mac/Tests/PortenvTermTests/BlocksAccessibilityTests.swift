// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftUI
import Testing

/// The cards driven through accessibility, as VoiceOver drives them
/// (R-0239, R-0243): a 2,400-line block's header pressed by its label, Show
/// All Output and "↑ Top of block". SwiftUI builds its accessibility tree
/// only for an assistive client, so the tests set the attributes VoiceOver
/// sets on the app, then press elements found by their labels.
/// Serialized: the attributes are the app's, shared by the tests.
@MainActor
@Suite(.serialized)
struct BlocksAccessibilityTests {
    static let esc = "\u{1b}"
    static func prompt(_ cmd: String) -> String { "\(esc)]133;A\u{07}$ \(esc)]133;B\u{07}\(cmd)\r\n\(esc)]133;C\u{07}" }
    static func end(_ code: Int) -> String { "\(esc)]133;D;\(code)\u{07}" }

    /// The board's build: 2,400 rows of output, then a running command.
    func reader() -> BlockReader {
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 900, height: 480))
        v.getTerminal().resize(cols: 110, rows: 30)
        var out = Self.prompt("git pull") + "Already up to date.\r\n" + Self.end(0)
        out += Self.prompt("npm run build") + "vite v6.0.3 building for production\u{2026}\r\n"
        for i in 1 ... 2398 { out += "transforming (\(i)) src/module-\(i).ts\r\n" }
        out += "built in 40.71s\r\n" + Self.end(0) + Self.prompt("claude --continue") + "working\r\n"
        v.ingest(Array(out.utf8))
        return BlockReader(view: v)
    }

    func assistive(_ on: Bool) {
        _ = NSApplication.shared
        for a in ["AXEnhancedUserInterface", "AXManualAccessibility"] {
            NSApp.accessibilitySetValue(on, forAttribute: NSAccessibility.Attribute(rawValue: a))
        }
    }

    func host(_ view: BlocksView) -> NSWindow {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 980, height: 640), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: view)
        window.makeKeyAndOrderFront(nil)
        return window
    }

    func settle() async { for _ in 0 ..< 10 { try? await Task.sleep(for: .milliseconds(30)) } }

    /// Every element under the window, with its role and label.
    func elements(_ window: NSWindow) -> [(role: String, label: String, element: AnyObject)] {
        var out: [(String, String, AnyObject)] = []
        func walk(_ e: AnyObject, _ depth: Int) {
            guard depth < 60 else { return }
            let role = (e.accessibilityRole?() ?? nil)?.rawValue ?? ""
            let label = (e.accessibilityLabel?() ?? nil) ?? ""
            out.append((role, label, e))
            for c in (e.accessibilityChildren?() ?? nil) ?? [] { walk(c as AnyObject, depth + 1) }
        }
        walk(window, 0)
        return out
    }

    /// The list's vertical scroll position (0 top, 1 bottom), as its
    /// scroll bar tells VoiceOver.
    func scrolled(_ window: NSWindow) -> Double? {
        for e in elements(window) where e.role == NSAccessibility.Role.scrollBar.rawValue {
            guard let o = e.element as? NSAccessibilityProtocol else { continue }
            let orientation = o.accessibilityOrientation()
            let value: Any? = o.accessibilityValue()
            if orientation == .vertical || orientation == .unknown, let v = value as? NSNumber { return v.doubleValue }
        }
        return nil
    }

    func press(_ label: String, in window: NSWindow) -> Bool? {
        guard let e = elements(window).first(where: { $0.label == label || $0.label.hasPrefix(label + ",") })?.element else { return nil }
        return e.accessibilityPerformPress?()
    }

    /// The header is a button labelled with its command, status and time,
    /// and pressing it through accessibility opens the 2,400-line block.
    @Test func aHeaderOpensThroughAccessibility() async {
        assistive(true)
        defer { assistive(false) }
        let reader = reader(), blocks = reader.blocks()
        let build = blocks[1], now = Date()
        let window = host(BlocksView(blocks: blocks, reader: reader, palette: .dark, now: now))
        defer { window.close() }
        await settle()
        let header = elements(window).first { $0.label.hasPrefix("npm run build") }
        #expect(header?.role == NSAccessibility.Role.button.rawValue)
        #expect(header?.label == BlockText.spoken(build, now: now))
        // The main thread's CPU time (threadCPUTime): the press's work, not
        // the wait for a core on a busy test machine.
        var pressed: Bool?
        let took = threadCPUTime { pressed = press("npm run build", in: window) }
        #expect(pressed == true)
        await settle()
        #expect(elements(window).contains { $0.label == BlockText.showAllOutput })
        #expect(took < .milliseconds(250))
    }

    /// Show All Output presses through accessibility, and the open block
    /// builds only the rows on screen, so VoiceOver's tree stays small.
    @Test func showAllOutputPressesThroughAccessibility() async {
        assistive(true)
        defer { assistive(false) }
        let reader = reader(), blocks = reader.blocks()
        let window = host(BlocksView(blocks: blocks, reader: reader, palette: .dark, toggled: [blocks[1].id]))
        defer { window.close() }
        await settle()
        #expect(press(BlockText.showAllOutput, in: window) == true)
        await settle()
        var after: [(role: String, label: String, element: AnyObject)] = []
        let walk = threadCPUTime { after = elements(window) }
        #expect(!after.contains { $0.label == BlockText.showAllOutput })
        #expect(after.count < 400, "\(after.count) elements: the rows aren't lazy")
        #expect(walk < .milliseconds(500))
    }

    /// "↑ Top of block" presses through accessibility and goes to the top
    /// of the block: its header no longer needs to stay at the top.
    @Test func topOfBlockScrollsThroughAccessibility() async {
        assistive(true)
        defer { assistive(false) }
        let reader = reader(), blocks = reader.blocks()
        let build = blocks[1]
        let window = host(BlocksView(blocks: blocks, reader: reader, palette: .dark, toggled: [build.id], showAll: [build.id],
                                     startAt: build.output!.lowerBound + 1200))
        defer { window.close() }
        await settle()
        #expect(elements(window).contains { $0.label == "Top of block" })
        let before = scrolled(window)
        #expect(press("Top of block", in: window) == true)
        // Up to 5 s for the list to get there on a slow runner.
        for _ in 0 ..< 170 where (scrolled(window) ?? 1) >= 0.1 {
            try? await Task.sleep(for: .milliseconds(30))
        }
        let after = scrolled(window)
        // The list went up to the block's top: the build is the second
        // block, near the top of the list. (Its sticky header unpinning
        // follows the window's layout passes, which a display-less CI
        // runner doesn't run after a programmatic scroll; on a Mac it
        // unpins, as the external accessibility check showed.)
        #expect((after ?? 1) < 0.1, "scroll \(String(describing: before)) → \(String(describing: after))")
        #expect(elements(window).contains { $0.label.hasPrefix("npm run build") })
    }
}
