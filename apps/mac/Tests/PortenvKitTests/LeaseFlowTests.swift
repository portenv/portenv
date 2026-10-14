// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Opening a workspace that's open somewhere else, and Move To's
/// confirmation, through BoxController (2.3).
@MainActor
struct LeaseFlowTests {
    private let server = Placement.Holder.knownServer(id: "srv-0123456789ab", name: "test-server")

    /// The refusal shows the sheet at once, naming the place and checking;
    /// the probe then fills it in. Nothing opens here meanwhile.
    @Test func aRefusalShowsTheSheetThenTheProbeFillsIt() async {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date().addingTimeInterval(-180), answers: .yes)
        let c = BoxController(box: "acme-api", daemon: daemon)
        daemon.onPlacement = { probe in
            if probe { await MainActor.run { #expect(c.leaseSheet?.kind == .checking) } }
        }
        await c.open()
        #expect(c.leaseSheet?.kind == .openThere)
        #expect(c.leaseSheet?.defaultAction == .connect)
        #expect(c.error == nil) // a sheet, not an error line
        #expect(daemon.calls.filter { $0 == ["app", "placement", "acme-api"] }.count == 1)
        #expect(daemon.calls.contains(["app", "placement", "acme-api", "--probe"]))
    }

    @Test func connectAttachesThere() async {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date(), answers: .yes)
        let c = BoxController(box: "acme-api", daemon: daemon)
        await c.open()
        daemon.openElsewhere = nil
        await c.choose(.connect)
        #expect(c.leaseSheet == nil)
        #expect(daemon.calls.contains(["app", "open", "acme-api", "--connect-there"]))
    }

    @Test func moveHereFromAServerThatAnswersMovesIt() async {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date(), answers: .yes)
        let c = BoxController(box: "acme-api", daemon: daemon)
        await c.open()
        daemon.openElsewhere = nil
        await c.choose(.moveHere)
        #expect(daemon.calls.contains(["app", "move", "acme-api", "this-mac"]))
    }

    /// Move to This Mac when the place doesn't answer takes it over (rule
    /// 4); Keep Waiting keeps trying the open until it works.
    @Test func moveToThisMacTakesOverAndKeepWaitingRetries() async throws {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: Date(), answers: .cannotAsk)
        let c = BoxController(box: "acme-api", daemon: daemon)
        c.keepWaitingEvery = .milliseconds(20)
        await c.open()
        #expect(c.leaseSheet?.defaultAction == .keepWaiting)
        await c.choose(.keepWaiting)
        #expect(c.leaseSheet == nil)
        #expect(c.waitingFor == "studio-1a2b3c")
        try await Task.sleep(for: .milliseconds(80))
        daemon.openElsewhere = nil // the other Mac let go
        for _ in 0..<50 where c.waitingFor != nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(c.waitingFor == nil)
        #expect(daemon.calls.filter { $0 == ["app", "open", "acme-api"] }.count >= 3)

        let taker = FakeDaemon()
        taker.openElsewhere = Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: Date(), answers: .cannotAsk)
        let t = BoxController(box: "acme-api", daemon: taker)
        await t.open()
        taker.openElsewhere = nil
        await t.choose(.moveHereTakingOver)
        #expect(taker.calls.contains(["app", "open", "acme-api", "--take-over"]))
    }

    /// Keep Waiting on a server that comes back: the sheet returns,
    /// offering Connect, and the waiting stops.
    @Test func keepWaitingSaysWhenTheServerAnswers() async throws {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date(), answers: .no)
        let c = BoxController(box: "acme-api", daemon: daemon)
        c.keepWaitingEvery = .milliseconds(20)
        await c.open()
        await c.choose(.keepWaiting)
        #expect(c.waitingFor == "test-server")
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date(), answers: .yes)
        for _ in 0..<50 where c.leaseSheet == nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(c.leaseSheet?.kind == .openThere)
        #expect(c.waitingFor == nil)
    }

    /// Cancel leaves it where it is: nothing opens or moves.
    @Test func cancelLeavesItThere() async {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: server, newestSaveAt: Date(), answers: .yes)
        let c = BoxController(box: "acme-api", daemon: daemon)
        await c.open()
        c.cancelLeaseSheet()
        #expect(c.leaseSheet == nil)
        #expect(!daemon.calls.contains { $0.contains("--connect-there") || $0.contains("--take-over") || $0.first == "app" && $0.dropFirst().first == "move" })
    }

    /// Move To asks first, naming what runs; Move goes, Cancel doesn't.
    @Test func moveToAsksFirst() async {
        let daemon = FakeDaemon()
        daemon.setTabs([TabInfo(id: "@0", name: "claude", active: true, program: "claude", command: "claude --continue",
                                runningSince: Date().addingTimeInterval(-180))])
        let c = BoxController(box: "acme-api", daemon: daemon)
        await c.run(.move("ubuntu@test-server"))
        #expect(c.moveConfirmation?.running == ["claude --continue in the claude tab \u{00B7} 3 min"])
        #expect(!daemon.calls.contains { $0.count > 1 && $0[1] == "move" })
        c.cancelMove()
        #expect(c.moveConfirmation == nil)
        await c.run(.move("ubuntu@test-server"))
        await c.confirmMove()
        #expect(daemon.calls.contains(["app", "move", "acme-api", "ubuntu@test-server"]))
    }
}
