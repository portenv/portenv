// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Asks before the window it sits in closes (the close button, ⌘W, File ›
/// Close), so closing the last box window can close the box and show the
/// box list in place of the window going away (WindowClose; the owner,
/// 2026-10-11). SwiftUI owns the window's delegate: the guard stands in
/// front of it and passes everything else through.
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
        private var proxy: WindowDelegateProxy?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            install()
        }

        /// Puts the proxy in front of the window's delegate, again if
        /// SwiftUI replaced it.
        func install() {
            guard let window else { return }
            if let proxy, window.delegate === proxy { return }
            let proxy = WindowDelegateProxy(original: window.delegate) { [weak self] window in
                self?.shouldClose?(window) ?? true
            }
            self.proxy = proxy
            window.delegate = proxy
        }
    }

    /// The windows a guard sits in, still open (to count box windows).
    @MainActor static var guardedWindows: [NSWindow] {
        NSApp.windows.filter { $0.isVisible && $0.delegate is WindowDelegateProxy }
    }
}
