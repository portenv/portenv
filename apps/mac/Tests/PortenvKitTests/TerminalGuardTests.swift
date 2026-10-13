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
            let (view, guardian) = TerminalViews.attached()
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
        let (view, guardian) = TerminalViews.attached()
        view.setFrameSize(NSSize(width: 600, height: 400))
        #expect(view.terminalDelegate === guardian)
    }

    // MARK: Links (R-0134)

    @Test func onlyWebLinksCanOpen() {
        for ok in ["https://example.com/a?b=c", "http://localhost:3000", "HTTPS://Example.com"] {
            #expect(TerminalLinks.openable(ok) != nil, "\(ok)")
        }
        for refused in ["file:///etc/passwd", "file:///Users/me/Downloads/run.command", "vscode://file/x", "x-apple.systempreferences:com.apple.Security",
                        "javascript:alert(1)", "ssh://host", "https://", "/tmp/x", "example.com", ""] {
            #expect(TerminalLinks.openable(refused) == nil, "\(refused)")
        }
        #expect(TerminalLinks.status("https://example.com") == "Opens https://example.com (⌘-click)")
        #expect(TerminalLinks.status("file:///etc/passwd") == "Portenv opens only web links: file:///etc/passwd")
    }

    /// The link shown is the link that opens: no control or bidi characters
    /// to hide or reorder part of it (R-0135).
    @Test func controlAndBidiCharactersAreNeverShown() {
        let tricky = "https://example.com/\u{202E}gpj.exe\u{202C}\u{2066}x\u{2069}\u{200F}\u{07}\u{1b}[2K\r\n\u{85}"
        let shown = TerminalLinks.display(tricky)
        #expect(!shown.unicodeScalars.contains { TerminalLinks.hidden($0) }, "\(shown.debugDescription)")
        #expect(shown == "https://example.com/gpj.exex%5B2K")
        #expect(TerminalLinks.status(tricky) == "Opens https://example.com/gpj.exex%5B2K (⌘-click)")
        // A refused link is cleaned too.
        #expect(TerminalLinks.status("file:///\u{202E}fdp.exe") == "Portenv opens only web links: file:///fdp.exe")
    }

    /// A look-alike domain shows as what it is: the host in punycode.
    @Test func theHostShowsInPunycode() {
        #expect(TerminalLinks.display("https://аpple.com/login") == "https://xn--pple-43d.com/login")
        #expect(TerminalLinks.status("https://аpple.com/login") == "Opens https://xn--pple-43d.com/login (⌘-click)")
        #expect(TerminalLinks.display("https://bücher.example/") == "https://xn--bcher-kva.example/")
        #expect(TerminalLinks.display("https://apple.com/login") == "https://apple.com/login")
        // What opens is the same ASCII URL that was shown.
        #expect(TerminalLinks.openable("https://аpple.com/login")?.absoluteString == "https://xn--pple-43d.com/login")
    }

    /// A link opens only on the person's own click, and only a web page:
    /// `file:` and a custom scheme are refused even then.
    @Test func aLinkOpensOnlyOnTheClickAndOnlyAWebPage() {
        _ = NSApplication.shared
        let (view, guardian) = TerminalViews.attached()
        var opened: [URL] = [], refused: [String] = []
        guardian.open = { opened.append($0) }
        guardian.onRefusedLink = { refused.append($0) }

        // Not a click (a program's doing): nothing opens.
        guardian.isPersonsClick = { _ in false }
        guardian.requestOpenLink(source: view, link: "https://example.com", params: [:])
        #expect(opened.isEmpty && refused == ["https://example.com"])

        // The person's click: a web page opens; file: and a custom scheme don't.
        guardian.isPersonsClick = { _ in true }
        for link in ["https://example.com", "file:///etc/passwd", "vscode://file/x"] {
            guardian.requestOpenLink(source: view, link: link, params: [:])
        }
        #expect(opened == [URL(string: "https://example.com")!])
        #expect(refused == ["https://example.com", "file:///etc/passwd", "vscode://file/x"])
    }

    /// With no event being handled (nothing the person did), the default
    /// check refuses.
    @Test func withoutAClickTheDefaultCheckRefuses() {
        _ = NSApplication.shared
        let (view, guardian) = TerminalViews.attached()
        var opened = 0
        guardian.open = { _ in opened += 1 }
        guardian.requestOpenLink(source: view, link: "https://example.com", params: [:])
        #expect(opened == 0)
    }

    /// Hovering finds the link's full destination: an OSC 8 link labelled
    /// one thing shows where it really goes.
    @Test func hoverFindsTheRealDestination() throws {
        _ = NSApplication.shared
        let (view, guardian) = TerminalViews.attached()
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
        #expect(TerminalLinks.openable(found ?? "") == nil)
        // Over plain text, nothing.
        #expect(guardian.link(atWindowPoint: view.convert(NSPoint(x: view.bounds.width - 30, y: 4), to: nil)) == nil)
    }

    @Test func theFactoryInstallsTheGuard() {
        let (view, guardian) = TerminalViews.attached()
        #expect(view.terminalDelegate === guardian)
    }
}
