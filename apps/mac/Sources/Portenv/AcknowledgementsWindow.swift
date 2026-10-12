// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// Help › Acknowledgements… (R-0096): one plain window with the third-party
/// notices as selectable, scrollable text. No web view.
@MainActor
enum AcknowledgementsWindow {
    private static var window: NSWindow?

    static func show() {
        if let window {
            window.makeKeyAndOrderFront(nil)
            return
        }
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 720, height: 520),
                         styleMask: [.titled, .closable, .miniaturizable, .resizable],
                         backing: .buffered, defer: false)
        w.title = Acknowledgements.windowTitle
        w.contentView = AcknowledgementsText.view(Acknowledgements.text(in: .main))
        w.contentMinSize = NSSize(width: 420, height: 300)
        w.isReleasedWhenClosed = false
        w.tabbingMode = .disallowed
        w.center()
        window = w
        w.makeKeyAndOrderFront(nil)
    }

    /// File › Close (⌘W) closes this window when it's in front, rather than a
    /// tab of the box behind it.
    static func closeIfKey() -> Bool {
        guard let window, window.isKeyWindow else { return false }
        window.performClose(nil)
        return true
    }
}
