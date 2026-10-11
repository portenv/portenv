// SPDX-License-Identifier: Apache-2.0

import AppKit
import Testing

@testable import PortenvKit

/// Stands in for SwiftUI's window delegate: records the callbacks it gets.
@MainActor
final class RecordingDelegate: NSObject, NSWindowDelegate {
    var events: [String] = []
    var allowClose = true
    func windowShouldClose(_: NSWindow) -> Bool { events.append("shouldClose"); return allowClose }
}

/// The close hook asks before a window closes without replacing the
/// window's delegate, which SwiftUI needs to size its content (R-0080).
@MainActor
struct WindowCloseHookTests {
    func window() -> NSWindow {
        _ = NSApplication.shared
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 400, height: 300),
                         styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: true)
        w.isReleasedWhenClosed = false
        return w
    }

    /// SwiftUI's delegate stays the window's delegate, and the content
    /// still follows the window's frame as it grows.
    @Test func theDelegateStaysAndTheContentFollowsTheFrame() {
        let w = window(), original = RecordingDelegate()
        w.delegate = original
        WindowCloseHook.install(on: w) { _ in true }
        WindowCloseHook.install(on: w) { _ in true } // SwiftUI updates again
        #expect(w.delegate === original)

        w.setFrame(NSRect(x: 0, y: 0, width: 2400, height: 1300), display: false)
        #expect(w.contentView?.frame.size == w.contentRect(forFrameRect: w.frame).size)
    }

    /// The close button asks the hook first; SwiftUI's delegate is asked
    /// only when the hook lets the window close, and its no still counts.
    @Test func theCloseButtonAsksTheHookThenTheDelegate() throws {
        let w = window(), original = RecordingDelegate()
        w.delegate = original
        var allow = false, asked = 0
        WindowCloseHook.install(on: w) { _ in asked += 1; return allow }
        w.makeKeyAndOrderFront(nil)
        defer { w.orderOut(nil) }
        let close = try #require(w.standardWindowButton(.closeButton))
        // What a click (or AXPress) sends.
        func click() { NSApp.sendAction(try! #require(close.action), to: close.target, from: close) }

        click()
        #expect(asked == 1)
        #expect(original.events.isEmpty)
        #expect(w.isVisible)

        allow = true
        original.allowClose = false
        click()
        #expect(asked == 2)
        #expect(original.events == ["shouldClose"])
        #expect(w.isVisible)

        original.allowClose = true
        WindowCloseHook.requestClose(w) // ⌘W's path
        #expect(asked == 3)
        #expect(!w.isVisible)
    }

    /// Only visible windows with a hook count as box windows.
    @Test func hookedWindowsAreTheVisibleOnes() {
        let a = window(), b = window(), plain = window()
        WindowCloseHook.install(on: a) { _ in true }
        WindowCloseHook.install(on: b) { _ in true }
        a.orderFront(nil)
        plain.orderFront(nil)
        defer { a.orderOut(nil); plain.orderOut(nil) }
        let hooked = WindowCloseHook.hookedWindows
        #expect(hooked.contains { $0 === a })
        #expect(!hooked.contains { $0 === b })
        #expect(!hooked.contains { $0 === plain })
    }

    /// macOS's own window tabs are off: a hooked window never joins a
    /// native tab group, and the app turns automatic tabbing off (R-0081).
    @Test func nativeWindowTabsAreOff() {
        let w = window()
        w.tabbingMode = .preferred
        WindowCloseHook.install(on: w) { _ in true }
        #expect(w.tabbingMode == .disallowed)

        let before = NSWindow.allowsAutomaticWindowTabbing
        defer { NSWindow.allowsAutomaticWindowTabbing = before }
        NSWindow.allowsAutomaticWindowTabbing = true
        WindowCloseHook.turnOffNativeTabs()
        #expect(!NSWindow.allowsAutomaticWindowTabbing)
    }

    /// A closed window's hook goes with it, the close button works again
    /// without one, and installing on the reused window makes a new hook
    /// that asks again (R-0084).
    @Test func closingTheWindowDropsItsHook() throws {
        let w = window()
        let close = try #require(w.standardWindowButton(.closeButton))
        let nativeTarget = close.target, nativeAction = close.action
        var asked = 0
        let first = WindowCloseHook.install(on: w) { _ in asked += 1; return true }
        #expect(WindowCloseHook.hook(for: w) === first)
        w.makeKeyAndOrderFront(nil)

        WindowCloseHook.requestClose(w)
        #expect(asked == 1)
        #expect(!w.isVisible)
        #expect(WindowCloseHook.hook(for: w) == nil)
        #expect(!WindowCloseHook.hookedWindows.contains { $0 === w })
        #expect(close.target === nativeTarget)
        #expect(close.action == nativeAction)

        let second = WindowCloseHook.install(on: w) { _ in asked += 1; return false }
        #expect(second !== first)
        #expect(WindowCloseHook.hook(for: w) === second)
        w.makeKeyAndOrderFront(nil)
        defer { w.orderOut(nil) }
        #expect(WindowCloseHook.hookedWindows.contains { $0 === w })
        NSApp.sendAction(try #require(close.action), to: close.target, from: close)
        #expect(asked == 2)
        #expect(w.isVisible)
    }
}
