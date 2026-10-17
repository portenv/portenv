// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftUI
import Testing

/// Clicks in an open block's body (R-0272, R-0273): on its output and on
/// its footer, a long block's and a short failed one's. A click there
/// selects text at most: it never closes the window or quits the app. In
/// BlocksAccessibilityTests' serialized suite: it finds where to click
/// through accessibility, whose switches are the app's.
extension BlocksAccessibilityTests {
    /// A short failed npm test, then a 2,360-line build, then a running
    /// command.
    func clickReader() -> BlockReader {
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 900, height: 480))
        v.getTerminal().resize(cols: 110, rows: 30)
        var out = Self.prompt("npm test") + "FAIL src/retry.test.ts\r\n  expected 3 retries, got 2\r\nTests: 1 failed, 41 passed\r\n" + Self.end(1)
        out += Self.prompt("npm run build") + "vite v6.0.3 building for production\u{2026}\r\n"
        for i in 1 ... 2358 { out += "transforming (\(i)) src/module-\(i).ts\r\n" }
        out += "built in 40.71s\r\n" + Self.end(0) + Self.prompt("claude --continue") + "working\r\n"
        v.ingest(Array(out.utf8))
        return BlockReader(view: v)
    }

    /// Every element's text (label, or value for static text) and frame on
    /// screen.
    func frames(_ window: NSWindow) -> [(text: String, frame: NSRect)] {
        elements(window).map { e in
            let value = (e.element as? NSAccessibilityProtocol)?.accessibilityValue() as? String ?? ""
            return (e.label.isEmpty ? value : e.label, e.element.accessibilityFrame?() ?? .zero)
        }
    }

    /// A left click at a point in the window. A press on selectable text
    /// tracks the mouse in a loop of its own until it reads the up, as with
    /// a real click, so the up waits in `SyntheticUp` for that loop to read
    /// (posting it with postEvent would stop the main run loop, and could
    /// end the test run with tests unfinished and exit 0). A press that
    /// doesn't track gets the up straight after.
    func click(_ window: NSWindow, at p: NSPoint) {
        func event(_ type: NSEvent.EventType) -> NSEvent {
            NSEvent.mouseEvent(with: type, location: p, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                               windowNumber: window.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)!
        }
        SyntheticUp.install()
        let up = event(.leftMouseUp)
        SyntheticUp.pending = up
        window.sendEvent(event(.leftMouseDown))
        if SyntheticUp.pending != nil {
            SyntheticUp.pending = nil
            window.sendEvent(up)
        }
    }

    /// Clicks on the output and the footer of the long block and of the
    /// short failed one leave the window open and the app running. A click
    /// on selectable text makes macOS close a text input window of its own,
    /// which is why a dev window quits only on its own close
    /// (QuitWithWindow).
    @Test func clicksInABlockBodyKeepTheWindowAndTheApp() async throws {
        _ = NSApplication.shared
        let reader = clickReader(), blocks = reader.blocks()
        let test = blocks[0], build = blocks[1]
        #expect(test.state == .failed(exit: 1))
        let window = NSWindow(contentRect: NSRect(x: 200, y: 200, width: 980, height: 640), styleMask: [.titled, .closable],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: BlocksView(blocks: blocks, reader: reader, palette: .dark,
                                                                toggled: [build.id]))
        // As `Portenv --blocks-preview` watches its window.
        var quits = 0
        let watch = QuitWithWindow.watch(window) { quits += 1 }
        defer { NotificationCenter.default.removeObserver(watch) }
        final class Spy: NSObject, NSApplicationDelegate {
            var asked = 0
            func applicationShouldTerminate(_: NSApplication) -> NSApplication.TerminateReply { asked += 1; return .terminateCancel }
        }
        let spy = Spy(), before = NSApp.delegate
        NSApp.delegate = spy
        defer { NSApp.delegate = before }
        window.makeKeyAndOrderFront(nil)
        defer { window.close() }
        await settle()

        assistive(true)
        await settle()
        let all = frames(window)
        assistive(false)
        func point(_ label: String, below: CGFloat = 0) throws -> NSPoint {
            let f = try #require(all.first { $0.text.hasPrefix(label) }?.frame, "no \(label)")
            // The element's middle (screen, from the bottom), or a little
            // below its bottom: the body's padding, its footer.
            return window.convertPoint(fromScreen: NSPoint(x: f.midX, y: below > 0 ? f.minY - below : f.midY))
        }
        let targets = try [
            ("the failed test's output", point("  expected 3 retries")),
            ("the failed test's footer", point("Tests: 1 failed", below: 5)),
            ("the build's output", point("transforming (3)")),
            ("the build's footer", point("\u{2026} 2,320 lines hidden")),
        ]
        for (what, p) in targets {
            #expect(window.contentView!.bounds.contains(p), "\(what) is off the window at \(p)")
            click(window, at: p)
            await settle()
            #expect(window.isVisible, "the window went after a click on \(what)")
            #expect(quits == 0, "a click on \(what) quit the app")
            #expect(spy.asked == 0, "a click on \(what) asked the app to quit")
        }
    }
}

/// The mouse up for a test's click: AppKit's next-event call hands it to the
/// first loop that asks for a mouse up (a press tracking the mouse), ahead
/// of the queue, and otherwise behaves as before.
@MainActor
enum SyntheticUp {
    nonisolated(unsafe) static var pending: NSEvent?
    private static var installed = false

    static func install() {
        guard !installed else { return }
        installed = true
        let sel = #selector(NSApplication.nextEvent(matching:until:inMode:dequeue:))
        guard let method = class_getInstanceMethod(NSApplication.self, sel) else { return }
        typealias Next = @convention(c) (AnyObject, Selector, UInt64, NSDate?, NSString, Bool) -> NSEvent?
        let original = unsafeBitCast(method_getImplementation(method), to: Next.self)
        let next: @convention(block) (AnyObject, UInt64, NSDate?, NSString, Bool) -> NSEvent? = { app, mask, until, mode, dequeue in
            if let up = pending, mask & (UInt64(1) << up.type.rawValue) != 0 {
                if dequeue { pending = nil }
                return up
            }
            return original(app, sel, mask, until, mode, dequeue)
        }
        method_setImplementation(method, imp_implementationWithBlock(next))
    }
}
