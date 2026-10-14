// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// The factory every terminal view comes from (R-0134, R-0140).
@MainActor
struct TerminalViewsTests {
    @Test func theAttachedViewHasItsGuard() {
        let (view, guardian) = TerminalViews.attached()
        #expect(view.terminalDelegate === guardian)
    }

    /// The tab view drops the emulator's replies and the clipboard from
    /// its first frame (QueryReplyTests has the full list).
    @Test func theTabViewIsGuardedFromTheStart() {
        _ = NSApplication.shared
        let view = TerminalViews.tab()
        let s = ScriptedTabSource(tab: "@1")
        view.attach(s)
        view.apply(.output(Array("\u{1b}[c\u{1b}]52;c;?\u{07}".utf8)))
        #expect(s.sent.isEmpty)
        #expect(view.terminalDelegate != nil)
    }
}
