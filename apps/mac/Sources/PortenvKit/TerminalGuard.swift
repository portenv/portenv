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
    /// What the state line shows for a link the guard refused: its status
    /// (TerminalLinks.status), cleaned, never the raw link (R-0142).
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
            guard isPersonsClick(view), let url = TerminalLinks.destination(link) else {
                onRefusedLink(TerminalLinks.status(link))
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
/// and the tooltip say about them (R-0134, R-0135, R-0140). One decision,
/// `verdict`, serves the tooltip, the state line and the open, so what's
/// shown is what opens.
public enum TerminalLinks {
    public enum Verdict: Equatable, Sendable {
        /// A web page, as the ASCII URL that's shown and opened (the host in
        /// punycode, the rest percent-encoded).
        case opens(URL)
        /// A control or bidi character somewhere in it, which could hide or
        /// reorder part of what's shown.
        case hiddenCharacters
        /// A user name (or password) before the host: `https://apple.com@evil.com/`
        /// reads as apple.com and goes to evil.com. Shown as its ASCII URL.
        case userInfo(String)
        /// Not http or https with a host: `file:`, `javascript:`, any app's
        /// custom scheme. Shown as it is (it has no hidden characters).
        case notWeb(String)
    }

    public static func verdict(_ link: String) -> Verdict {
        if link.unicodeScalars.contains(where: hidden) { return .hiddenCharacters }
        guard let url = URL(string: link), let scheme = url.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = url.host, !host.isEmpty
        else { return .notWeb(link) }
        if url.user != nil || url.password != nil { return .userInfo(url.absoluteString) }
        return .opens(url)
    }

    /// The URL a click opens, or nil: Portenv opens nothing else.
    public static func destination(_ link: String) -> URL? {
        if case let .opens(url) = verdict(link) { return url }
        return nil
    }

    /// What the tooltip and the state line show for the link under the
    /// pointer, before anyone clicks: the full destination that opens, or
    /// why it won't.
    public static func status(_ link: String) -> String {
        switch verdict(link) {
        case let .opens(url): "Opens \(url.absoluteString) (⌘-click)"
        case .hiddenCharacters: "Portenv won't open a link with hidden characters"
        case let .userInfo(shown): "Portenv won't open a link with a user name before the address: \(shown)"
        case let .notWeb(shown): "Portenv opens only web links: \(shown)"
        }
    }

    /// Control characters (C0, DEL, C1) and the bidi controls: the
    /// embeddings and overrides (U+202A–U+202E), the isolates
    /// (U+2066–U+2069), and the marks (U+200E, U+200F, U+061C).
    static func hidden(_ c: Unicode.Scalar) -> Bool {
        switch c.value {
        case 0x00 ... 0x1F, 0x7F ... 0x9F, 0x202A ... 0x202E, 0x2066 ... 0x2069, 0x200E, 0x200F, 0x061C: true
        default: false
        }
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
