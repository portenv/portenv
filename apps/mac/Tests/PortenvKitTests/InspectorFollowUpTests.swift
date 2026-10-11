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
        #expect(SavedTime.nextChange(noon, now: noon.addingTimeInterval(60)) == nil)
        #expect(SavedTime.nextChange(nil, now: noon) == nil)
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
        await until { c.savedTimeNow >= saved.addingTimeInterval(60) }
        #expect(c.subtitle(at: c.savedTimeNow).hasPrefix("This Mac · Saved at "))
        let content = InspectorContent(box: c, tabs: TabsController(box: "acme-api", daemon: d))
        #expect(SavedTime.inspector(saved, now: content.now) == "Last saved " + StateLine.clock(saved, timeZone: .current, locale: .current))
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

    // MARK: Move To… says why it can't

    @Test func moveToSaysWhyWhenThereIsNowhereToMove() {
        #expect(InspectorText.moveUnavailable(targets: []) == "Add a server to move this box.")
        #expect(InspectorText.moveUnavailable(targets: ["This Mac"]) == nil)
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
    func advance(_ d: Duration) {
        lock.withLock { _now = _now.addingTimeInterval(Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18) }
    }
}
