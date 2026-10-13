// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
@testable import PortenvKit
import SwiftTerm
import Testing

/// No program in the workspace reads or sets the Mac's clipboard through the
/// app's terminal (OSC 52; R-0133). The test puts a marker on the clipboard
/// and restores whatever was there before.
@MainActor
struct ClipboardGuardTests {
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
            let view = LocalProcessTerminalView(frame: NSRect(x: 0, y: 0, width: 400, height: 300))
            let guardian = ClipboardGuard.install(on: view)
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
        let view = LocalProcessTerminalView(frame: NSRect(x: 0, y: 0, width: 400, height: 300))
        let guardian = ClipboardGuard.install(on: view)
        _ = guardian
        view.setFrameSize(NSSize(width: 600, height: 400))
        #expect(view.terminalDelegate === guardian)
    }
}
