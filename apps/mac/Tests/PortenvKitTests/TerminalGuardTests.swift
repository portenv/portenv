// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
@testable import PortenvKit
import SwiftTerm
import Testing

/// No program in the workspace reads or sets the Mac's clipboard through the
/// app's terminal (OSC 52; R-0133), or opens a link by itself, or opens
/// anything but a web page (R-0134). The clipboard tests put a marker on the
/// clipboard and restore whatever was there before.
@MainActor
struct TerminalGuardTests {
    /// Runs `body` with the general pasteboard holding only `marker`, then
    /// puts the person's clipboard back.
    func withClipboard(_ marker: String, _ body: (NSPasteboard) throws -> Void) rethrows {
        let pb = NSPasteboard.general
        let saved: [[(NSPasteboard.PasteboardType, Data)]] = (pb.pasteboardItems ?? []).map { item in
            item.types.compactMap { t in item.data(forType: t).map { (t, $0) } }
        }
        defer {
            pb.clearContents()
            pb.writeObjects(saved.map { pairs in
                let item = NSPasteboardItem()
                for (t, d) in pairs { item.setData(d, forType: t) }
                return item
            })
        }
        pb.clearContents()
        pb.setString(marker, forType: .string)
        try body(pb)
    }

    /// What tmux sent the app for `tmux set-buffer -w` (recorded from tmux
    /// 3.4 with set-clipboard external), and a program's own write and query.
    func osc52(_ text: String) -> [UInt8] {
        let b64 = Data(text.utf8).base64EncodedString()
        return Array("\u{1b}]52;;\(b64)\u{07}\u{1b}]52;c;\(b64)\u{1b}\\\u{1b}]52;c;?\u{07}".utf8)
    }

    @Test func aProgramCantSetTheClipboard() {
        _ = NSApplication.shared
        withClipboard("PORTENV-CLIPBOARD-BEFORE") { pb in
            let (view, guardian) = guarded()
            let count = pb.changeCount
            view.feed(byteArray: osc52("PORTENV-FROM-THE-WORKSPACE")[...])
            #expect(pb.changeCount == count)
            #expect(pb.string(forType: .string) == "PORTENV-CLIPBOARD-BEFORE")
            #expect(view.terminalDelegate === guardian)
        }
    }

    /// Without the guard, SwiftTerm's view does set it: the test sees what
    /// the guard stops.
    @Test func withoutTheGuardTheViewWould() {
        _ = NSApplication.shared
        withClipboard("PORTENV-CLIPBOARD-BEFORE") { pb in
            let view = LocalProcessTerminalView(frame: NSRect(x: 0, y: 0, width: 400, height: 300))
            view.feed(byteArray: osc52("PORTENV-FROM-THE-WORKSPACE")[...])
            #expect(pb.string(forType: .string) == "PORTENV-FROM-THE-WORKSPACE")
        }
    }

    /// The guard forwards the rest: a size change still reaches the view's
    /// own handling, which notifies its process delegate.
    @Test func everythingElseStillReachesTheView() {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        view.setFrameSize(NSSize(width: 600, height: 400))
        #expect(view.terminalDelegate === guardian)
    }

    // MARK: Links (R-0134)

    @Test func onlyWebLinksCanOpen() {
        for ok in ["https://example.com/a?b=c", "http://localhost:3000", "HTTPS://Example.com"] {
            #expect(TerminalLinks.destination(ok) != nil, "\(ok)")
        }
        for refused in ["file:///etc/passwd", "file:///Users/me/Downloads/run.command", "vscode://file/x", "x-apple.systempreferences:com.apple.Security",
                        "javascript:alert(1)", "ssh://host", "https://", "/tmp/x", "example.com", ""] {
            #expect(TerminalLinks.destination(refused) == nil, "\(refused)")
        }
        #expect(TerminalLinks.status("https://example.com") == "Opens https://example.com (⌘-click)")
        #expect(TerminalLinks.status("file:///etc/passwd") == "Portenv opens only web links: file:///etc/passwd")
    }

    /// Clicks every link in `links` as the person would, and returns what
    /// was handed to open.
    func click(_ links: [String]) -> [URL] {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        var opened: [URL] = []
        guardian.open = { opened.append($0) }
        guardian.isPersonsClick = { _ in true }
        for link in links { guardian.requestOpenLink(source: view, link: link, params: [:]) }
        return opened
    }

    /// What's shown is what opens (R-0140): for every link that opens, the
    /// URL handed to open is exactly the one the state line shows.
    @Test func theURLThatOpensIsTheOneShown() {
        let links = ["https://example.com/gpj.exe", "https://аpple.com/login", "https://bücher.example/päth?q=ü",
                     "https://example.com/a b", "HTTPS://Example.com/x#frag", "http://localhost:3000/"]
        for link in links {
            let opened = click([link])
            #expect(opened.count == 1, "\(link) didn't open")
            if let url = opened.first {
                #expect(TerminalLinks.status(link) == "Opens \(url.absoluteString) (⌘-click)", "\(link)")
                #expect(!url.absoluteString.unicodeScalars.contains { $0.value > 0x7E }, "\(url) isn't ASCII")
            }
        }
    }

    /// A link with any hidden character (control or bidi) isn't opened at
    /// all, and the state line says so (R-0140).
    @Test func aLinkWithHiddenCharactersNeverOpens() {
        let tricky = [
            "https://example.com/\u{202E}gpj.exe\u{202C}",       // right-to-left override in the path
            "https://exa\u{202E}mple.com/",                       // in the host
            "https://example.com/\u{2066}x\u{2069}\u{200F}",      // isolates and a mark
            "https://example.com/\u{07}\u{1b}[2K\r\n",           // BEL, ESC, CR, LF
            "https://example.com/\u{85}x",                        // C1 NEL
            "https://ex\u{00}ample.com@evil.com/",                // NUL in the authority
            "https://example.com/\u{061C}", "https://example.com/\u{7F}",
        ]
        #expect(click(tricky).isEmpty)
        for link in tricky {
            #expect(TerminalLinks.destination(link) == nil, "\(link.debugDescription)")
            #expect(TerminalLinks.status(link) == "Portenv won't open a link with hidden characters", "\(link.debugDescription)")
        }
    }

    /// A user name before the host reads as one site and goes to another.
    @Test func aLinkWithAUserNameNeverOpens() {
        let links = ["https://apple.com@evil.com/", "https://user:pass@example.com/", "http://@example.com/"]
        #expect(click(links).isEmpty)
        #expect(TerminalLinks.status("https://apple.com@evil.com/")
            == "Portenv won't open a link with a user name before the address. It goes to evil.com.")
        // The real destination, in punycode; never the user name or password.
        #expect(TerminalLinks.status("https://user:hunter2@аpple.com/")
            == "Portenv won't open a link with a user name before the address. It goes to xn--pple-43d.com.")
        for link in ["https://user:hunter2@example.com/", "https://me:s3cr%21t@example.com/x", "http://:p4ss@example.com/"] {
            let shown = TerminalLinks.status(link)
            for secret in ["hunter2", "s3cr", "p4ss", "user:", "me:"] {
                #expect(!shown.contains(secret), "\(shown) shows \(secret)")
            }
        }
        for link in links { #expect(TerminalLinks.destination(link) == nil, "\(link)") }
    }

    /// A look-alike domain shows as what it is: the host in punycode.
    @Test func theHostShowsInPunycode() {
        #expect(TerminalLinks.status("https://аpple.com/login") == "Opens https://xn--pple-43d.com/login (⌘-click)")
        #expect(TerminalLinks.destination("https://аpple.com/login")?.absoluteString == "https://xn--pple-43d.com/login")
        #expect(TerminalLinks.destination("https://bücher.example/")?.absoluteString == "https://xn--bcher-kva.example/")
        #expect(TerminalLinks.destination("https://apple.com/login")?.absoluteString == "https://apple.com/login")
    }

    /// A link opens only on the person's own click, and only a web page:
    /// `file:` and a custom scheme are refused even then.
    @Test func aLinkOpensOnlyOnTheClickAndOnlyAWebPage() {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        var opened: [URL] = [], refused: [String] = []
        guardian.open = { opened.append($0) }
        guardian.onRefusedLink = { refused.append($0) }

        // Not a click (a program's doing): nothing opens.
        guardian.isPersonsClick = { _ in false }
        guardian.requestOpenLink(source: view, link: "https://example.com", params: [:])
        #expect(opened.isEmpty && refused == [TerminalLinks.status("https://example.com")])

        // The person's click: a web page opens; file: and a custom scheme don't.
        guardian.isPersonsClick = { _ in true }
        for link in ["https://example.com", "file:///etc/passwd", "vscode://file/x"] {
            guardian.requestOpenLink(source: view, link: link, params: [:])
        }
        #expect(opened == [URL(string: "https://example.com")!])
        #expect(refused == [TerminalLinks.status("https://example.com"),
                            "Portenv opens only web links: file:///etc/passwd", "Portenv opens only web links: vscode://file/x"])
    }

    /// A refused link reaches the state line cleaned, never raw (R-0142).
    @Test func aRefusedLinkReachesTheStateLineCleaned() {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        var shown: [String] = []
        guardian.onRefusedLink = { shown.append($0) }
        guardian.isPersonsClick = { _ in true }
        let raw = ["https://example.com/\u{202E}gpj.exe", "file:///x\u{1b}[2J\u{07}", "https://apple.com@evil.com/\u{2066}",
                   "https://user:pass@example.com/"]
        for link in raw { guardian.requestOpenLink(source: view, link: link, params: [:]) }
        #expect(shown == ["Portenv won't open a link with hidden characters", "Portenv won't open a link with hidden characters",
                          "Portenv won't open a link with hidden characters",
                          "Portenv won't open a link with a user name before the address. It goes to example.com."])
        for text in shown {
            #expect(!text.unicodeScalars.contains { TerminalLinks.hidden($0) }, "\(text.debugDescription)")
            #expect(!raw.contains(text))
        }
    }

    /// With no event being handled (nothing the person did), the default
    /// check refuses.
    @Test func withoutAClickTheDefaultCheckRefuses() {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        var opened = 0
        guardian.open = { _ in opened += 1 }
        guardian.requestOpenLink(source: view, link: "https://example.com", params: [:])
        #expect(opened == 0)
    }

    /// Hovering finds the link's full destination: an OSC 8 link labelled
    /// one thing shows where it really goes.
    @Test func hoverFindsTheRealDestination() throws {
        _ = NSApplication.shared
        let (view, guardian) = guarded()
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 640, height: 400), styleMask: [.titled], backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        w.contentView = view
        view.frame = w.contentLayoutRect
        view.feed(text: "\u{1b}]8;;file:///Users/me/evil.command\u{1b}\\docs.example.com\u{1b}]8;;\u{1b}\\ plain")
        // Find a window point over the first cell, the way the mouse would.
        var found: String?
        for x in stride(from: 1.0, to: 60.0, by: 2.0) {
            let local = NSPoint(x: x, y: view.bounds.height - 4)
            if let link = guardian.link(atWindowPoint: view.convert(local, to: nil)) {
                found = link
                break
            }
        }
        #expect(found == "file:///Users/me/evil.command")
        #expect(TerminalLinks.destination(found ?? "") == nil)
        // Over plain text, nothing.
        #expect(guardian.link(atWindowPoint: view.convert(NSPoint(x: view.bounds.width - 30, y: 4), to: nil)) == nil)
    }

    /// A guarded view, as TerminalViews.attached() (PortenvTerm) makes it;
    /// its own test is in PortenvTermTests.
    func guarded() -> (view: LocalProcessTerminalView, guard: TerminalGuard) {
        let view = LocalProcessTerminalView(frame: .zero)
        let guardian = TerminalGuard(for: view)
        view.terminalDelegate = guardian
        return (view, guardian)
    }
}
