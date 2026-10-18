// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Testing

@testable import PortenvKit

/// The pane header's words (the split-view board) and where the dividers
/// are.
struct PaneHeaderTests {
    let utc = TimeZone(identifier: "UTC")!
    let now = Date(timeIntervalSince1970: 1_800_000_000) // 08:00 UTC

    /// The board's three headers.
    @Test func theBoardsHeaders() {
        let claude = TabInfo(id: "@1", name: "claude", active: true, program: "Claude Code",
                             waiting: TabQuestion(text: "Push fix/retries?", secret: false),
                             typingAuthor: TabAuthor(name: "Preston", isYou: true))
        #expect(PaneHeader(claude, now: now, timeZone: utc) == PaneHeader(detail: "Claude Code \u{00B7} asking you", who: "You"))

        let grok = TabInfo(id: "@2", name: "grok", active: false, program: "npm",
                           typingAuthor: TabAuthor(name: "Grok Bot", isYou: false))
        #expect(PaneHeader(grok, now: now, timeZone: utc) == PaneHeader(detail: "npm", who: "Grok Bot is typing"))

        let tests = TabInfo(id: "@3", name: "tests", active: false, program: "npm",
                            lastOutputAt: now.addingTimeInterval(-120), watchers: ["Claude Code"])
        let h24 = AppClock.locale(language: "en", hourCycle: .zeroToTwentyThree)
        #expect(PaneHeader(tests, now: now, timeZone: utc, locale: h24) == PaneHeader(detail: "watching", who: "quiet since 07:58"))
    }

    /// "Quiet since" in the app's language and the Mac's 12/24-hour
    /// setting (R-0172).
    @Test func quietSinceFollowsTheHourSetting() {
        let tests = TabInfo(id: "@3", name: "tests", active: false, program: "npm",
                            lastOutputAt: now.addingTimeInterval(-120 + 6 * 3600))
        let later = now.addingTimeInterval(6 * 3600)
        let h12 = AppClock.locale(language: "en", hourCycle: .oneToTwelve)
        let h24 = AppClock.locale(language: "en", hourCycle: .zeroToTwentyThree)
        let in12 = PaneHeader(tests, now: later, timeZone: utc, locale: h12).who
        let in24 = PaneHeader(tests, now: later, timeZone: utc, locale: h24).who
        #expect(in12.hasPrefix("quiet since 1:58") && in12.hasSuffix("PM"), "\(in12)")
        #expect(in24 == "quiet since 13:58")
        // The app's language picks the words around the numbers.
        #expect(AppClock.locale(language: "fr", hourCycle: .zeroToTwentyThree).language.languageCode == "fr")
    }

    @Test func aPlainShell() {
        #expect(PaneHeader(TabInfo(id: "@4", name: "dev", active: false), now: now, timeZone: utc)
            == PaneHeader(detail: "", who: ""))
    }

    /// VoiceOver's label joins them: "Pane: grok tab, Grok Bot is typing".
    @Test func theLabel() {
        let h = PaneHeader(detail: "npm", who: "Grok Bot is typing")
        #expect(h.label(tab: "grok") == "Pane: grok tab, npm, Grok Bot is typing")
        #expect(PaneHeader(detail: "", who: "").label(tab: "dev") == "Pane: dev tab")
    }

    /// The dividers: the root's, then the right column's.
    @Test func dividers() {
        var l = SplitLayout(tab: "a")
        let size = CGSize(width: 1400, height: 900)
        _ = l.split(.right, showing: "b", in: size)
        _ = l.split(.down, showing: "c", in: size)
        let d = l.dividers(in: size)
        #expect(d.map(\.path) == [[], [1]])
        #expect(d[0].axis == .right && d[0].line == CGRect(x: 700, y: 0, width: 0, height: 900))
        #expect(d[0].span == CGRect(x: 0, y: 0, width: 1400, height: 900))
        #expect(d[1].axis == .down && d[1].line == CGRect(x: 700, y: 450, width: 700, height: 0))
    }
}
