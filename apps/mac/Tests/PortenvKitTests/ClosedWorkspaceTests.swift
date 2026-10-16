// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A closed workspace's window (R-0211): Open, first in the title menu and
/// as the window's default button; a failed open stays in the window with
/// its plain line and Try Again, never only an alert or a silent "closed".
@MainActor
struct ClosedWorkspaceTests {
    func titles(_ items: [BoxMenuItem]) -> [String] { items.map(\.title) }

    @Test func aClosedWorkspacesMenuStartsWithOpen() {
        let closed = BoxMenuModel.items(open: false, busy: false, agentUnavailable: false, optionHeld: false, location: nil, servers: [])
        #expect(Array(titles(closed).prefix(3)) == ["Open", "—", "Rename…"])
        #expect(closed.first?.enabled == true && closed.first?.action == .open)
        let busy = BoxMenuModel.items(open: false, busy: true, agentUnavailable: false, optionHeld: false, location: nil, servers: [])
        #expect(busy.first?.enabled == false)
        let open = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [])
        #expect(!titles(open).contains("Open"))
    }

    @Test func openFromTheMenuOpensIt() async {
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d)
        await c.run(.open)
        #expect(d.calls.contains { $0.count > 1 && $0[1] == "open" })
    }

    /// A failed open: the window keeps portenvd's plain line, no alert, and
    /// Try Again opens; a good open clears the line.
    @Test func aFailedOpenStaysInTheWindow() async {
        let d = FakeDaemon()
        d.failing["open"] = "The workspace failed to start."
        d.state = #"{"state":"SAVE_STATE_CLOSED"}"# // what portenvd says: nothing opened
        let c = BoxController(box: "acme-api", daemon: d)
        await c.open()
        #expect(c.location == .closed)
        #expect(c.openFailure == "The workspace failed to start.")
        #expect(c.error == nil, "not an alert")
        d.failing["open"] = nil
        d.state = #"{"state":"SAVE_STATE_NOT_SAVED_YET"}"#
        await c.open()
        #expect(c.openFailure == nil)
        #expect(c.location == .thisMac)
    }

    /// A failure that isn't an open's still goes to the alert.
    @Test func otherFailuresStillAlert() async {
        let d = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: d)
        await c.open()
        d.failing["point"] = "The workspace helper is unavailable."
        await c.makeSavePoint()
        #expect(c.error == "The workspace helper is unavailable.")
        #expect(c.openFailure == nil)
    }

    @Test func theWindowsWords() {
        #expect(ClosedText.title("acme-api") == "acme-api is closed")
        #expect(ClosedText.buttonLabel("acme-api", failed: false) == "Open acme-api")
        #expect(ClosedText.buttonLabel("acme-api", failed: true) == "Try opening acme-api again")
        #expect(!KeychainWait.cancelled.contains("'"))
    }
}
