// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The reviewer's follow-ups on the inspector (1.2): one rule for "Saved …",
/// why Move To… is unavailable, the This Mac encryption line, saves that
/// can't be loaded, and a box that moves back to this Mac.
@MainActor
struct InspectorFollowUpTests {
    private let noon = Date(timeIntervalSince1970: 1_791_288_000) // 12:00 UTC
    private let utc = TimeZone(identifier: "UTC")!
    private let gb = Locale(identifier: "en_GB")

    // MARK: One source and one rule for "Saved …"

    @Test func theTitleAndTheInspectorSaySavedWithTheSameRule() {
        // GUIDELINES §3.1: "Saved just now" for the first minute, then
        // "Saved at <time>"; the inspector's "Last saved" follows it.
        let cases: [(TimeInterval, String, String)] = [
            (0, "Saved just now", "Last saved just now"),
            (59, "Saved just now", "Last saved just now"),
            (60, "Saved at 12:00", "Last saved 12:00"),
            (120, "Saved at 12:00", "Last saved 12:00"),
        ]
        for (after, title, inspector) in cases {
            let now = noon.addingTimeInterval(after)
            #expect(SavedTime.stateLine(noon, now: now, timeZone: utc, locale: gb) == title)
            #expect(SavedTime.inspector(noon, now: now, timeZone: utc, locale: gb) == inspector)
            #expect(BoxState(save: .saved, savedAt: noon).line(now: now, timeZone: utc, locale: gb) == "This Mac · \(title)")
        }
    }

    @Test func theWordsChangeOnceAMinuteAfterTheSave() {
        // Views redraw then, so "Saved just now" never outlives its minute
        // (it once still said so two minutes after the save).
        #expect(SavedTime.nextChange(noon, now: noon) == noon.addingTimeInterval(60))
        #expect(SavedTime.nextChange(noon, now: noon.addingTimeInterval(59)) == noon.addingTimeInterval(60))
        // After the minute, the next change is midnight ("yesterday at").
        #expect(SavedTime.nextChange(noon, now: noon.addingTimeInterval(60), timeZone: utc, locale: gb)
            == noon.addingTimeInterval(12 * 3600))
        #expect(SavedTime.nextChange(nil, now: noon) == nil)
    }

    // MARK: "Saved yesterday at …" (R-0061)

    private func utcDate(_ s: String) -> Date { ISO8601DateFormatter().date(from: s)! }

    @Test func olderSavesSayTheDay() {
        // GUIDELINES §3.1: today the time; the next day "yesterday at" the
        // time; then the date (with the year once it's another year).
        let saved = utcDate("2026-10-09T23:43:00Z")
        let cases: [(String, String, String)] = [
            ("2026-10-09T23:50:00Z", "Saved at 23:43", "Last saved 23:43"),
            ("2026-10-10T00:00:00Z", "Saved yesterday at 23:43", "Last saved yesterday at 23:43"),
            ("2026-10-10T23:59:59Z", "Saved yesterday at 23:43", "Last saved yesterday at 23:43"),
            ("2026-10-11T00:00:00Z", "Saved on 9 Oct at 23:43", "Last saved on 9 Oct at 23:43"),
            ("2026-12-31T23:59:59Z", "Saved on 9 Oct at 23:43", "Last saved on 9 Oct at 23:43"),
            ("2027-01-01T00:00:00Z", "Saved on 9 Oct 2026 at 23:43", "Last saved on 9 Oct 2026 at 23:43"),
        ]
        for (now, title, inspector) in cases {
            #expect(SavedTime.stateLine(saved, now: utcDate(now), timeZone: utc, locale: gb) == title, "\(now)")
            #expect(SavedTime.inspector(saved, now: utcDate(now), timeZone: utc, locale: gb) == inspector, "\(now)")
        }
        // A save stamped ahead of this Mac's clock is "just now", never a
        // day: it can't have been yesterday.
        #expect(SavedTime.stateLine(saved, now: saved.addingTimeInterval(-120), timeZone: utc, locale: gb) == "Saved just now")
    }

    @Test func theWordsChangeAtMidnightAndAtTheNewYear() {
        let saved = utcDate("2026-10-09T23:43:00Z")
        let next = { (now: String) in SavedTime.nextChange(saved, now: self.utcDate(now), timeZone: self.utc, locale: self.gb) }
        #expect(next("2026-10-09T23:50:00Z") == utcDate("2026-10-10T00:00:00Z"))
        #expect(next("2026-10-10T05:00:00Z") == utcDate("2026-10-11T00:00:00Z"))
        #expect(next("2026-10-11T00:00:00Z") == utcDate("2027-01-01T00:00:00Z"))
        #expect(next("2027-01-01T00:00:00Z") == nil)
    }

    @Test func daysFollowTheCalendarAcrossTheClockChange() {
        // Paris leaves summer time on 25 Oct 2026: that day lasts 25 hours,
        // so its midnight is 23:00 UTC, not 24 hours after the last one.
        let paris = TimeZone(identifier: "Europe/Paris")!
        let saved = utcDate("2026-10-24T21:30:00Z") // 23:30 in Paris
        #expect(SavedTime.stateLine(saved, now: utcDate("2026-10-25T10:00:00Z"), timeZone: paris, locale: gb)
            == "Saved yesterday at 23:30")
        #expect(SavedTime.nextChange(saved, now: utcDate("2026-10-25T10:00:00Z"), timeZone: paris, locale: gb)
            == utcDate("2026-10-25T23:00:00Z"))
        #expect(SavedTime.stateLine(saved, now: utcDate("2026-10-25T22:59:59Z"), timeZone: paris, locale: gb)
            == "Saved yesterday at 23:30")
        #expect(SavedTime.stateLine(saved, now: utcDate("2026-10-25T23:00:00Z"), timeZone: paris, locale: gb)
            == "Saved on 24 Oct at 23:30")
    }

    /// Midnight turns "Saved at 23:43" into "Saved yesterday at 23:43" with
    /// no new state, from the controller's clock (as "just now" does).
    @Test func midnightRollsOverWithNoNewState() async {
        var parts = DateComponents(year: 2026, month: 10, day: 9, hour: 23, minute: 43)
        parts.timeZone = .current
        let saved = Calendar.current.date(from: parts)!
        let clock = FakeClock(saved.addingTimeInterval(300))
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d, now: { clock.now }, sleep: { clock.advance($0) })
        await c.open()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"\#(ISO8601DateFormatter().string(from: saved))"}"#
        await c.refresh()
        let time = StateLine.clock(saved, timeZone: .current, locale: .current)
        #expect(c.subtitle(at: c.savedTimeNow) == "This Mac · Saved at \(time)")
        await until { clock.woke.count >= 2 }
        let midnight = Calendar.current.date(byAdding: .day, value: 1, to: Calendar.current.startOfDay(for: saved))!
        #expect(clock.woke.first == midnight)
        #expect(c.subtitle(at: midnight) == "This Mac · Saved yesterday at \(time)")
        #expect(SavedTime.inspector(saved, now: midnight) == "Last saved yesterday at \(time)")
    }

    @Test func theTitleUsesTheTimeItIsDrawnAt() async {
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d)
        await c.open()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-06T12:00:00Z"}"#
        await c.refresh()
        let saved = ISO8601DateFormatter().date(from: "2026-10-06T12:00:00Z")!
        #expect(c.subtitle(at: saved.addingTimeInterval(30)) == "This Mac · Saved just now")
        #expect(c.subtitle(at: saved.addingTimeInterval(120)).hasPrefix("This Mac · Saved at "))
        #expect(c.savedAtChangesAt(saved.addingTimeInterval(30)) == saved.addingTimeInterval(60))
    }

    /// R-0066: "just now" rolls over a minute after the save with no new
    /// state, in the title and the inspector, driven by the controller's
    /// clock rather than a view's schedule.
    @Test func justNowRollsOverWithNoNewState() async {
        let saved = ISO8601DateFormatter().date(from: "2026-10-06T12:00:00Z")!
        let clock = FakeClock(saved.addingTimeInterval(10))
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d, now: { clock.now }, sleep: { clock.advance($0) })
        await c.open()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-06T12:00:00Z"}"#
        await c.refresh()
        #expect(c.subtitle(at: c.savedTimeNow) == "This Mac · Saved just now")
        // The ticker wakes a minute after the save; the words then are the
        // time. (It goes on to midnight and beyond: those have their test.)
        await until { !clock.woke.isEmpty }
        let woke = clock.woke[0]
        #expect(woke == saved.addingTimeInterval(60))
        #expect(c.subtitle(at: woke).hasPrefix("This Mac · Saved at "))
        #expect(SavedTime.inspector(saved, now: woke) == "Last saved " + StateLine.clock(saved, timeZone: .current, locale: .current))
        await until { c.savedTimeNow >= woke }
        let content = InspectorContent(box: c, tabs: TabsController(box: "acme-api", daemon: d))
        #expect(content.now == c.savedTimeNow)
    }

    /// R-0066: a new save reads the saves again at once, so the list keeps
    /// up with the state line.
    @Test func aNewSaveReadsTheSavesAgain() async {
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d)
        await c.open()
        await c.refreshSaves()
        let reads = { d.calls.filter { $0 == ["app", "saves", "acme-api"] }.count }
        let before = reads()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-06T12:00:00Z"}"#
        await c.refresh()
        await until { reads() > before }
        #expect(reads() > before)
        // The same state again is not a new save: no further read.
        let after = reads()
        await c.refresh()
        for _ in 0..<50 { await Task.yield() }
        #expect(reads() == after)
    }

    private func until(_ done: @MainActor () -> Bool) async {
        for _ in 0..<500 where !done() { await Task.yield() }
    }

    // MARK: The board's words (R-0142)

    @Test func runningNowShowsTheTabsOnOneRow() {
        let rows = InspectorText.tabRows([
            TabInfo(id: "@0", name: "claude", active: true, program: "claude"),
            TabInfo(id: "@1", name: "shell", active: false),
            TabInfo(id: "@2", name: "muse", active: false),
        ])
        #expect(InspectorText.tabsLabel == "Tabs")
        #expect(InspectorText.tabNames(rows) == "claude, shell, muse")
    }

    @Test func whosHereHasTheTypographicApostrophe() {
        #expect(InspectorText.whosHere == "Who\u{2019}s here")
    }

    // MARK: Move To… is never a dead end

    /// With no server, Move To… still opens: This Mac checked, then Add a
    /// Server… (R-0142, move-and-lease), the title menu's own submenu.
    @Test func moveToOpensEvenWithNoServer() {
        let none = BoxMenuModel.moveSubmenu(busy: false, location: "this-mac", servers: [])
        #expect(none.map(\.title) == ["This Mac", "—", "Add a Server…"])
        #expect(none.first?.checked == true && none.first?.enabled == true)
        let some = BoxMenuModel.moveSubmenu(busy: false, location: "this-mac", servers: ["portenv@test-server"])
        #expect(some.map(\.title) == ["This Mac", "—", "test-server", "—", "Add a Server…"])
        #expect(some[2].enabled && !some[2].checked)
    }

    // MARK: Encryption, This Mac

    @Test func savesKeptOnThisMacDontLeaveIt() {
        #expect(InspectorText.encryption(nil, savesTo: "this Mac") == "Encrypted on this Mac")
        #expect(InspectorText.encryption(nil, savesTo: "your server") == "Encrypted on this Mac before they leave it")
        #expect(InspectorText.encryption(nil, savesTo: "your bucket") == "Encrypted on this Mac before they leave it")
        #expect(InspectorText.encryption("test-server", savesTo: "your server") == "Encrypted on test-server before they leave it")
    }

    // MARK: Saves that can't be loaded

    @Test func savesThatCantBeLoadedSaySoWithTryAgain() {
        let row = InspectorText.saveRows([SaveInfo(id: "p", time: noon, kind: "point", machine: "mac")])
        #expect(InspectorText.savesShown(loaded: false, failed: true, rows: [], elsewhere: nil) == .failed)
        #expect(InspectorText.savesShown(loaded: false, failed: false, rows: [], elsewhere: nil) == .loading)
        // Saves already listed stay listed while a later read fails.
        #expect(InspectorText.savesShown(loaded: true, failed: true, rows: row, elsewhere: nil) == .listed)
        #expect(InspectorText.savesFailed == "Couldn't load saves.")
        #expect(InspectorText.tryAgain == "Try Again")
    }

    @Test func aFailedReadIsShownAndTriedAgainQuietly() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED"}"#
        d.saves = [SaveInfo(id: "x", time: noon, kind: "point", machine: "mac")]
        d.failingOnce["saves"] = "portenvd took too long"
        let c = BoxController(box: "acme-api", daemon: d, savesRetry: .milliseconds(10))
        await c.refresh()
        await c.refreshSaves()
        #expect(c.savesFailed)
        #expect(!c.savesLoaded)
        // Keeps trying until portenvd answers.
        d.failingOnce["saves"] = "portenvd took too long"
        await c.keepSavesLoaded()
        #expect(!c.savesFailed)
        #expect(c.savesLoaded)
        #expect(c.saves.map(\.id) == ["x"])
        #expect(d.calls.filter { $0 == ["app", "saves", "acme-api"] }.count == 3)
    }

    // MARK: Back to this Mac

    @Test func movingBackToThisMacLoadsTheSavesAgain() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED","location":"test-server"}"#
        let c = BoxController(box: "acme-api", daemon: d)
        await c.refresh()
        await c.refreshSaves()
        #expect(c.savesLoaded) // listed on the server
        d.state = #"{"state":"SAVE_STATE_SAVED"}"#
        await c.refresh()
        #expect(c.location == .thisMac)
        #expect(!c.savesLoaded, "not \"No saves yet.\" before portenvd answers")
        #expect(InspectorText.savesShown(loaded: c.savesLoaded, failed: c.savesFailed,
                                         rows: InspectorText.saveRows(c.saves), elsewhere: nil) == .loading)
    }
}

/// A clock tests move by hand: sleeping moves it on at once.
final class FakeClock: @unchecked Sendable {
    private let lock = NSLock()
    private var _now: Date
    init(_ now: Date) { _now = now }
    var now: Date { lock.withLock { _now } }
    private var _woke: [Date] = []
    /// The time after each sleep, in order.
    var woke: [Date] { lock.withLock { _woke } }
    func advance(_ d: Duration) {
        lock.withLock {
            _now = _now.addingTimeInterval(Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18)
            _woke.append(_now)
        }
    }
}
