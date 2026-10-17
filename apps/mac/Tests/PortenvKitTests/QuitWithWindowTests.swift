// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import Testing

/// A dev window that is the whole app quits it when it closes, and only
/// then: macOS's own windows in the app (the text input window a click on
/// selectable text brings) close too, and closed the app (R-0272, R-0273).
@MainActor
struct QuitWithWindowTests {
    func window() -> NSWindow {
        _ = NSApplication.shared
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 200), styleMask: [.titled, .closable],
                         backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        return w
    }

    @Test func onlyItsOwnWindowQuits() {
        let own = window(), other = window()
        var quits = 0
        let token = QuitWithWindow.watch(own) { quits += 1 }
        defer { NotificationCenter.default.removeObserver(token) }
        own.orderFront(nil)
        other.orderFront(nil)
        other.close()
        #expect(quits == 0, "another window closing quit the app")
        own.close()
        #expect(quits == 1)
    }
}
