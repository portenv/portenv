// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv

/// Keep Waiting ends when the window closes, not whenever its view
/// disappears (R-0179): a view can leave the window (the service notice,
/// another state) while the window, and the wait, stay.
@MainActor
struct WindowCloseWatchTests {
    private func settle() async {
        for _ in 0..<6 { try? await Task.sleep(for: .milliseconds(20)) }
    }

    @Test func endsOnlyWhenTheWindowCloses() async {
        _ = NSApplication.shared
        final class Count: @unchecked Sendable { var n = 0 }
        let closes = Count()
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 200), styleMask: [.titled, .closable],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let host = NSHostingView(rootView: Color.clear.background(EndsWithWindow { closes.n += 1 }))
        window.contentView = host
        window.orderFront(nil)
        await settle()
        // The view leaves the window, which stays: nothing ends.
        window.contentView = NSView()
        await settle()
        #expect(closes.n == 0)
        // Back in a window that then closes: it ends once.
        let window2 = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 200), styleMask: [.titled, .closable],
                               backing: .buffered, defer: false)
        window2.isReleasedWhenClosed = false
        window2.contentView = NSHostingView(rootView: Color.clear.background(EndsWithWindow { closes.n += 1 }))
        window2.orderFront(nil)
        await settle()
        window2.close()
        await settle()
        #expect(closes.n == 1)
        window.close()
    }

    /// Stop Waiting has Command-period, the Mac's "stop" (R-0179).
    @Test func stopWaitingHasCommandPeriod() {
        #expect(StopWaiting.shortcut == KeyboardShortcut(".", modifiers: .command))
    }
}
