// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI

/// Runs once when the window this view is in closes: not when the view
/// only leaves it (another state, the service notice), so a wait the
/// window started isn't ended while the window stays (R-0179).
struct EndsWithWindow: NSViewRepresentable {
    let onClose: () -> Void

    func makeNSView(context _: Context) -> Watcher { Watcher(onClose: onClose) }
    func updateNSView(_ view: Watcher, context _: Context) { view.onClose = onClose }

    final class Watcher: NSView {
        var onClose: () -> Void

        init(onClose: @escaping () -> Void) {
            self.onClose = onClose
            super.init(frame: .zero)
        }

        required init?(coder _: NSCoder) { nil }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            // Selector-based: removed with the view, no token to keep.
            NotificationCenter.default.removeObserver(self, name: NSWindow.willCloseNotification, object: nil)
            guard let window else { return }
            NotificationCenter.default.addObserver(self, selector: #selector(windowWillClose), name: NSWindow.willCloseNotification, object: window)
        }

        @objc private func windowWillClose(_: Notification) { onClose() }
    }
}

/// Keep Waiting's way out, and its key: Command-period, the Mac's "stop".
enum StopWaiting {
    static let title = "Stop Waiting"
    static let shortcut = KeyboardShortcut(".", modifiers: .command)
}
