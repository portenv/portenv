// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftTerm

/// What a program in the workspace can't make the app's terminal do
/// (R-0133, R-0134):
/// - read or set the Mac's clipboard (OSC 52);
/// - open a link by itself, or open anything but a web page.
///
/// SwiftTerm's LocalProcessTerminalView is its own delegate and does both:
/// an OSC 52 write goes onto the general pasteboard, a query is answered
/// with it, and a clicked link opens with whatever handles its scheme
/// (`file:`, any app's custom scheme). tmux 3.4's default `set-clipboard
/// external` forwarded `tmux set-buffer -w` from any program in the
/// workspace as OSC 52.
///
/// This stands in front of the view as its delegate: clipboard requests are
/// dropped; a link opens only when it's http or https with a host, and only
/// on the person's own click; everything else goes to the view as before.
/// TerminalViews makes every terminal view with one, and keeps it.
@MainActor
public final class TerminalGuard: TerminalViewDelegate {
    private weak var view: LocalProcessTerminalView?
    /// Opens a link that passed the checks (NSWorkspace; tests replace it).
    var open: (URL) -> Void = { NSWorkspace.shared.open($0) }
    /// Tells whether the event being handled is the person's own click in
    /// the view's window (tests replace it).
    var isPersonsClick: (NSView?) -> Bool = { view in
        guard let event = NSApp.currentEvent, event.type == .leftMouseUp else { return false }
        return view?.window != nil && event.window === view?.window
    }
    /// A link the guard refused, for the state line.
    public var onRefusedLink: (String) -> Void = { _ in }

    /// Puts a guard in front of the view and returns it.
    public static func install(on view: LocalProcessTerminalView) -> TerminalGuard {
        let g = TerminalGuard()
        g.view = view
        view.terminalDelegate = g
        return g
    }

    public nonisolated func clipboardCopy(source _: TerminalView, content _: Data) {}
    public nonisolated func clipboardRead(source _: TerminalView) -> Data? { nil }

    /// SwiftTerm asks this when the person clicks a link (⌘-click). It opens
    /// only a web page, and only on that click: a program can't make it.
    public nonisolated func requestOpenLink(source _: TerminalView, link: String, params _: [String: String]) {
        MainActor.assumeIsolated {
            guard isPersonsClick(view), let url = TerminalLinks.openable(link) else {
                onRefusedLink(link)
                return
            }
            open(url)
        }
    }

    /// The link under a point in the view's window: what the person would
    /// open by clicking there, or nil.
    public func link(atWindowPoint point: NSPoint) -> String? {
        guard let view else { return nil }
        let t = view.getTerminal()
        let local = view.convert(point, from: nil)
        guard view.bounds.contains(local), t.cols > 0 else { return nil }
        let index = view.characterIndex(for: point)
        let cell = Position(col: index % t.cols, row: index / t.cols)
        guard cell.row >= 0, cell.row < t.rows else { return nil }
        return t.link(at: .screen(cell), mode: .explicitAndImplicit)
    }

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

    public nonisolated func rangeChanged(source: TerminalView, startY: Int, endY: Int) {
        MainActor.assumeIsolated { view?.rangeChanged(source: source, startY: startY, endY: endY) }
    }
}

/// Which links a program printed Portenv will open, and what the state line
/// and the tooltip say about them (R-0134).
public enum TerminalLinks {
    /// A web page (http or https, with a host) Portenv may open; nil for
    /// anything else: `file:`, `javascript:`, any app's custom scheme.
    public static func openable(_ link: String) -> URL? {
        guard let url = URL(string: link), let scheme = url.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = url.host, !host.isEmpty
        else { return nil }
        return url
    }

    /// What the tooltip and the state line show for the link under the
    /// pointer: its full destination, before anyone clicks.
    public static func status(_ link: String) -> String {
        openable(link) != nil ? "Opens \(link) (⌘-click)" : "Portenv opens only web links: \(link)"
    }
}

/// Makes every terminal view the app shows, each with its guard (R-0134).
/// Nothing else in the app constructs one; `scripts/check-terminal-views.sh`
/// fails the build on a terminal view made anywhere else.
@MainActor
public enum TerminalViews {
    /// Today's attached terminal (`portenv attach` in a process), guarded.
    /// Keep the guard: the view holds its delegate weakly.
    public static func attached() -> (view: LocalProcessTerminalView, guard: TerminalGuard) {
        let view = LocalProcessTerminalView(frame: .zero)
        return (view, TerminalGuard.install(on: view))
    }
}
