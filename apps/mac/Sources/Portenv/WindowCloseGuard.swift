// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Asks before the window it sits in closes (the close button, ⌘W), so
/// closing the last box window can close the box and show the box list in
/// place of the window going away (WindowClose; the owner, 2026-10-11).
/// SwiftUI keeps the window's delegate (WindowCloseHook).
struct WindowCloseGuard: NSViewRepresentable {
    /// Whether the window may close now; false keeps it open.
    let shouldClose: @MainActor (NSWindow) -> Bool

    func makeNSView(context _: Context) -> GuardView {
        let view = GuardView()
        view.shouldClose = shouldClose
        return view
    }

    func updateNSView(_ view: GuardView, context _: Context) {
        view.shouldClose = shouldClose
        view.install()
    }

    final class GuardView: NSView {
        var shouldClose: (@MainActor (NSWindow) -> Bool)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            install()
        }

        func install() {
            guard let window else { return }
            WindowCloseHook.install(on: window) { [weak self] window in
                self?.shouldClose?(window) ?? true
            }
        }
    }
}
