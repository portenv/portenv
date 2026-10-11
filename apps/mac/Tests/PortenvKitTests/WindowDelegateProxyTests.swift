// SPDX-License-Identifier: Apache-2.0

import AppKit
import Testing

@testable import PortenvKit

/// Stands in for SwiftUI's window delegate: records the callbacks it gets.
@MainActor
final class RecordingDelegate: NSObject, NSWindowDelegate {
    var events: [String] = []
    var allowClose = true
    func windowDidResize(_: Notification) { events.append("didResize") }
    func windowDidBecomeKey(_: Notification) { events.append("didBecomeKey") }
    func windowWillMiniaturize(_: Notification) { events.append("willMiniaturize") }
    func windowWillResize(_: NSWindow, to size: NSSize) -> NSSize { events.append("willResize"); return size }
    func windowShouldClose(_: NSWindow) -> Bool { events.append("shouldClose"); return allowClose }
}

/// The close guard stands in front of SwiftUI's window delegate (R-0061):
/// every other callback still reaches the original delegate.
@MainActor
struct WindowDelegateProxyTests {
    func window() -> NSWindow {
        NSWindow(contentRect: NSRect(x: 0, y: 0, width: 400, height: 300),
                 styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: true)
    }

    /// Real window notifications and direct callbacks reach the original
    /// delegate with the proxy installed as the window's delegate.
    @Test func theOriginalsOtherCallbacksStillFire() {
        let w = window(), original = RecordingDelegate()
        w.delegate = original
        let proxy = WindowDelegateProxy(original: w.delegate) { _ in true }
        w.delegate = proxy
        #expect(w.delegate === proxy)
        #expect(proxy.original === original)

        w.setContentSize(NSSize(width: 500, height: 350)) // posts didResize
        NotificationCenter.default.post(name: NSWindow.didBecomeKeyNotification, object: w)
        NotificationCenter.default.post(name: NSWindow.willMiniaturizeNotification, object: w)
        let size = w.delegate?.windowWillResize?(w, to: NSSize(width: 10, height: 20))
        #expect(size == NSSize(width: 10, height: 20))
        #expect(original.events.contains("didResize"))
        #expect(original.events.contains("didBecomeKey"))
        #expect(original.events.contains("willMiniaturize"))
        #expect(original.events.contains("willResize"))
        // What the original doesn't implement, the proxy doesn't claim.
        #expect(!proxy.responds(to: #selector(NSWindowDelegate.windowDidMove(_:))))
    }

    /// The guard's answer comes first; the original is asked only when the
    /// guard lets the window close, and its no still keeps it open.
    @Test func closingAsksTheGuardThenTheOriginal() {
        let w = window(), original = RecordingDelegate()
        var allow = false
        let proxy = WindowDelegateProxy(original: original) { _ in allow }
        w.delegate = proxy
        #expect(w.delegate?.windowShouldClose?(w) == false)
        #expect(original.events.isEmpty)
        allow = true
        #expect(w.delegate?.windowShouldClose?(w) == true)
        #expect(original.events == ["shouldClose"])
        original.allowClose = false
        #expect(w.delegate?.windowShouldClose?(w) == false)
    }
}
