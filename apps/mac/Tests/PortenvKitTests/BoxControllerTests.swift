// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

let utc = TimeZone(identifier: "UTC")!

/// Each state line comes from the recorded state alone (the full table is
/// in GuidelinesTests).
struct StateLineTests {
    let gb = Locale(identifier: "en_GB")
    let later = ISO8601DateFormatter().date(from: "2026-10-08T12:00:00Z")!

    @Test func parsesPortenvdState() {
        let s = BoxState.parse(#"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T09:42:01.123456Z","location":"portenv@server-a"}"#)
        #expect(s?.save == .saved)
        #expect(s?.location == "portenv@server-a")
        #expect(s?.line(now: later, timeZone: utc, locale: gb) == "server-a · Saved at 09:42")
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_NOT_SAVED_YET"}"#)?.line() == "This Mac · Not saved yet")
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_RETRYING","saved_at":"2026-10-08T09:42:00Z"}"#)?.save == .retrying)
        #expect(BoxState.parse("not json") == nil)
    }
}

@MainActor
struct BoxControllerTests {
    /// A successful open says nothing about saves: a new box shows "Not
    /// saved yet" because that is what portenvd records.
    @Test func aNewBoxShowsNotSavedYet() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        #expect(c.location == .thisMac)
        #expect(c.subtitle == "This Mac · Not saved yet")
        #expect(c.servers == ["portenv@server-a"])
    }

    /// The window never says "is closed" between a successful open and the
    /// terminal (it once did while asking portenvd where the box runs).
    @Test func neverClosedWhileOpening() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        let seen = Seen()
        cli.onRun = { args in
            guard args.count > 1, args[1] == "state" else { return }
            await MainActor.run { seen.add(c.busy || c.location != .closed ? "ok" : "says closed") }
        }
        await c.open()
        #expect(!seen.all.contains("says closed"), "\(seen.all)")
        #expect(c.location == .thisMac)
    }

    @Test func theStateLineFollowsRecordedState() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        for (json, line) in [
            (#"{"state":"SAVE_STATE_SAVING"}"#, "This Mac · Saving…"),
            (#"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T09:42:00Z"}"#, "This Mac · Saved at " + Self.local("2026-10-08T09:42:00Z")),
            (#"{"state":"SAVE_STATE_OFFLINE"}"#, "This Mac · Offline · will save later"),
            (#"{"state":"SAVE_STATE_AGENT_UNAVAILABLE"}"#, "This Mac · Not saved"),
        ] {
            cli.state = json
            await c.refresh()
            #expect(c.subtitle == line)
        }
        #expect(c.agentUnavailable)
    }

    /// The current location is checked; the others stay available.
    @Test func moveToChecksTheCurrentLocation() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        #expect(c.isCurrent("this-mac") && !c.isCurrent("portenv@server-a"))
        cli.state = #"{"state":"SAVE_STATE_SAVED","location":"portenv@server-a"}"#
        await c.move(to: "portenv@server-a")
        #expect(c.location == .server("portenv@server-a"))
        #expect(c.isCurrent("portenv@server-a") && !c.isCurrent("this-mac"))
        #expect(c.terminalGeneration == 2) // the terminal reattaches on the server
        let moves = cli.calls.filter { $0.count > 1 && $0[1] == "move" }.count
        await c.move(to: "portenv@server-a") // choosing where it runs does nothing
        #expect(cli.calls.filter { $0.count > 1 && $0[1] == "move" }.count == moves)
    }

    /// Revert To and Restart Box work on a box on a server as on this Mac.
    @Test func revertAndRestartWorkOnAServerBox() async {
        let cli = FakeDaemon()
        cli.state = #"{"state":"SAVE_STATE_SAVED","location":"portenv@server-a"}"#
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        await c.revertToLastSavePoint()
        await c.restartBox()
        #expect(cli.calls.contains(["app", "revert", "demo"]))
        #expect(cli.calls.contains(["app", "restart", "demo"]))
        #expect(c.error == nil)
    }

    @Test func anUnavailableAgentIsReportedAndRestartIsOffered() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        cli.failing["point"] = "the box agent is unavailable (its channel is down or failed verification); restart the box"
        cli.state = #"{"state":"SAVE_STATE_AGENT_UNAVAILABLE"}"#
        await c.makeSavePoint()
        #expect(c.error?.contains("the box agent is unavailable") == true)
        #expect(c.agentUnavailable)
        #expect(c.isOpen)
    }

    @Test func closingSavesAndReleases() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        await c.close()
        #expect(cli.calls.last == ["app", "close", "demo"])
        #expect(c.location == .closed)
    }

    static func local(_ iso: String) -> String {
        // The user's own 12/24-hour setting, as the app shows it.
        StateLine.clock(ISO8601DateFormatter().date(from: iso)!, timeZone: .current, locale: .current)
    }
}
