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

    /// Workspace windows are macOS window tabs, in one group; any other
    /// hooked window never becomes a tab (R-0344, R-0345; R-0081 before).
    @Test func onlyWorkspaceWindowsAreTabs() {
        let w = window(), other = window()
        WindowCloseHook.install(on: w, tabs: true) { _ in true }
        #expect(w.tabbingMode == .preferred)
        #expect(w.tabbingIdentifier == WindowCloseHook.workspaceTabs)
        other.tabbingMode = .preferred
        WindowCloseHook.install(on: other) { _ in true }
        #expect(other.tabbingMode == .disallowed)
        // First run, then a workspace in the same window: it becomes one.
        WindowCloseHook.install(on: other, tabs: true) { _ in true }
        #expect(other.tabbingMode == .preferred)

        let before = NSWindow.allowsAutomaticWindowTabbing
        defer { NSWindow.allowsAutomaticWindowTabbing = before }
        NSWindow.allowsAutomaticWindowTabbing = false
        WindowCloseHook.turnOnWorkspaceTabs()
        #expect(NSWindow.allowsAutomaticWindowTabbing)
    }

    /// A tab behind another in the group isn't visible, but it's open: it
    /// counts as a box window, so closing the tab in front just closes it.
    @Test func aBackgroundTabCountsAsOpen() {
        let a = window(), b = window()
        WindowCloseHook.install(on: a, tabs: true) { _ in true }
        WindowCloseHook.install(on: b, tabs: true) { _ in true }
        a.orderFront(nil)
        a.addTabbedWindow(b, ordered: .above)
        b.makeKeyAndOrderFront(nil)
        defer { b.orderOut(nil); a.orderOut(nil) }
        #expect(WindowCloseHook.hookedWindows.contains { $0 === a })
        #expect(WindowCloseHook.hookedWindows.contains { $0 === b })
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
