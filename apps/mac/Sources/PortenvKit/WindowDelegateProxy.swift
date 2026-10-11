// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Stands in front of a window's delegate (SwiftUI's) to ask before the
/// window closes (WindowClose), and passes every other callback through to
/// the original delegate. Like NSWindow's own delegate reference, `original`
/// is weak: the window's owner (SwiftUI) keeps it alive.
@MainActor
public final class WindowDelegateProxy: NSObject, NSWindowDelegate {
    public nonisolated(unsafe) weak var original: NSWindowDelegate?
    let shouldClose: @MainActor (NSWindow) -> Bool

    public init(original: NSWindowDelegate?, shouldClose: @escaping @MainActor (NSWindow) -> Bool) {
        self.original = original
        self.shouldClose = shouldClose
    }

    override public nonisolated func responds(to selector: Selector!) -> Bool {
        super.responds(to: selector) || (original?.responds(to: selector) ?? false)
    }

    override public nonisolated func forwardingTarget(for selector: Selector!) -> Any? {
        original?.responds(to: selector) == true ? original : nil
    }

    public func windowShouldClose(_ sender: NSWindow) -> Bool {
        guard shouldClose(sender) else { return false }
        return original?.windowShouldClose?(sender) ?? true
    }
}
