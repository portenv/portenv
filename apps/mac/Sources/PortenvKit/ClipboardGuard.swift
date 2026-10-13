// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftTerm

/// The app's terminal never lets a program in the workspace read or set the
/// Mac's clipboard (OSC 52; R-0133).
///
/// SwiftTerm's LocalProcessTerminalView is its own delegate and does both: a
/// write goes onto the general pasteboard, and a query is answered with the
/// pasteboard's contents, typed back into the program. tmux 3.4's default
/// `set-clipboard external` forwarded `tmux set-buffer -w` from any program
/// in the workspace to it as OSC 52. This stands in front of the view as its
/// delegate: clipboard requests are dropped, everything else goes to the
/// view as before. Keep a reference to it; the view holds its delegate
/// weakly.
@MainActor
public final class ClipboardGuard: TerminalViewDelegate {
    private weak var view: LocalProcessTerminalView?

    /// Puts a guard in front of the view and returns it.
    public static func install(on view: LocalProcessTerminalView) -> ClipboardGuard {
        let g = ClipboardGuard()
        g.view = view
        view.terminalDelegate = g
        return g
    }

    public nonisolated func clipboardCopy(source _: TerminalView, content _: Data) {}
    public nonisolated func clipboardRead(source _: TerminalView) -> Data? { nil }

    public nonisolated func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        MainActor.assumeIsolated { view?.sizeChanged(source: source, newCols: newCols, newRows: newRows) }
    }

    public nonisolated func setTerminalTitle(source: TerminalView, title: String) {
        MainActor.assumeIsolated { view?.setTerminalTitle(source: source, title: title) }
    }

    public nonisolated func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {
        MainActor.assumeIsolated { view?.hostCurrentDirectoryUpdate(source: source, directory: directory) }
    }

    public nonisolated func send(source: TerminalView, data: ArraySlice<UInt8>) {
        MainActor.assumeIsolated { view?.send(source: source, data: data) }
    }

    public nonisolated func scrolled(source: TerminalView, position: Double) {
        MainActor.assumeIsolated { view?.scrolled(source: source, position: position) }
    }

    public nonisolated func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        MainActor.assumeIsolated { view?.requestOpenLink(source: source, link: link, params: params) }
    }

    public nonisolated func rangeChanged(source: TerminalView, startY: Int, endY: Int) {
        MainActor.assumeIsolated { view?.rangeChanged(source: source, startY: startY, endY: endY) }
    }
}
