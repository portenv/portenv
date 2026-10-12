// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import Testing

/// Edit › Find… shows the Acknowledgements window's find bar (R-0119,
/// R-0121): the menu's action, sent as Find… sends it, reaches the text view,
/// which shows the bar in its scroll view. The window is never shown.
@MainActor
struct AcknowledgementsFindTests {
    @Test func findShowsTheFindBar() throws {
        _ = NSApplication.shared
        let scroll = AcknowledgementsText.view("Portenv includes…\n\ncel.dev/expr v0.25.1 · Apache-2.0\n")
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 720, height: 520),
                              styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: true)
        window.isReleasedWhenClosed = false
        window.contentView = scroll
        let text = try #require(scroll.documentView as? NSTextView)
        #expect(text.usesFindBar)
        #expect(scroll.isFindBarVisible == false)

        let sender = FindMenu.sender(.showFindInterface)
        #expect(sender.tag == NSTextFinder.Action.showFindInterface.rawValue)
        text.performTextFinderAction(sender)
        #expect(text.enclosingScrollView?.isFindBarVisible == true)
    }

    /// The text view follows light and dark through the system's colours.
    @Test func usesTheSystemTextColours() throws {
        let text = try #require(AcknowledgementsText.view("x").documentView as? NSTextView)
        #expect(text.textColor == .textColor)
        #expect(text.backgroundColor == .textBackgroundColor)
    }
}
