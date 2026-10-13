// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The title menu against its board (R-0131): Revert To ▸ Last Save Point
/// shows the save point's time at its trailing edge (§3.2, "with its
/// time"), and can't be chosen when the saves are known and none is a
/// save point.
@MainActor
struct TitleMenuTests {
    let tz = TimeZone(identifier: "Europe/Paris")!
    let h24 = Locale(identifier: "en_GB")
    // 11:52 in Paris on 8 Oct.
    let point = ISO8601DateFormatter().date(from: "2026-10-08T09:52:00Z")!

    func lastSavePoint(_ items: [BoxMenuItem]) -> BoxMenuItem? {
        items.first { $0.title == "Revert To" }?.submenu.first { $0.title == "Last Save Point" }
    }

    @Test func theTimeReadsAsOnTheBoard() {
        #expect(SavedTime.menu(point, now: point.addingTimeInterval(3600), timeZone: tz, locale: h24) == "11:52 today")
        #expect(SavedTime.menu(point, now: point.addingTimeInterval(86400), timeZone: tz, locale: h24) == "yesterday at 11:52")
        #expect(SavedTime.menu(point, now: point.addingTimeInterval(5 * 86400), timeZone: tz, locale: h24) == "on 8 Oct at 11:52")
        #expect(SavedTime.menu(point, now: point.addingTimeInterval(3600), timeZone: tz, locale: Locale(identifier: "en_US"))
            .replacingOccurrences(of: "\u{202F}", with: " ") == "11:52 AM today")
    }

    @Test func lastSavePointShowsItsTime() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false,
                                       location: "this-mac", servers: [], lastSavePoint: point, savesKnown: true,
                                       now: point.addingTimeInterval(600))
        let item = lastSavePoint(items)
        #expect(item?.enabled == true)
        #expect(item?.detail?.hasSuffix(" today") == true)
    }

    @Test func noSavePointYetCantBeChosen() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false,
                                       location: "this-mac", servers: [], lastSavePoint: nil, savesKnown: true)
        #expect(lastSavePoint(items)?.enabled == false)
        #expect(lastSavePoint(items)?.detail == nil)
    }

    /// Saves not read yet (or listed on a server): the item stays as before,
    /// enabled with no time; portenvd says if there's nothing to revert to.
    @Test func unknownSavesLeaveItAvailable() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false,
                                       location: "portenv@test-server", servers: [])
        #expect(lastSavePoint(items)?.enabled == true)
        #expect(lastSavePoint(items)?.detail == nil)
    }

    @Test func theControllerUsesTheNewestSavePoint() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z"}"#
        d.saves = [
            SaveInfo(id: "a", time: point.addingTimeInterval(3600), kind: "autosave", machine: "mac"),
            SaveInfo(id: "p2", time: point, kind: "point", machine: "mac", name: "before retry change"),
            SaveInfo(id: "p1", time: point.addingTimeInterval(-7200), kind: "point", machine: "mac"),
        ]
        let c = BoxController(box: "acme-api", daemon: d)
        await c.refresh()
        #expect(lastSavePoint(c.menuItems(optionHeld: false))?.detail == nil)
        await c.refreshSaves()
        #expect(c.lastSavePoint == point)
        #expect(lastSavePoint(c.menuItems(optionHeld: false))?.detail != nil)
        d.saves = [SaveInfo(id: "a", time: point, kind: "autosave", machine: "mac")]
        await c.refreshSaves()
        #expect(lastSavePoint(c.menuItems(optionHeld: false))?.enabled == false)
    }
}
