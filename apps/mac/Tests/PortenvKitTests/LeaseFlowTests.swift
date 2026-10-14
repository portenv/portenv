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
        // At least one more try while it's still held, then the other Mac
        // lets go and the next try opens it.
        for _ in 0..<500 where daemon.calls.filter({ $0 == ["app", "open", "acme-api"] }).count < 2 {
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(c.waitingFor == "studio-1a2b3c")
        daemon.openElsewhere = nil
        for _ in 0..<500 where c.waitingFor != nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(c.waitingFor == nil)

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

    /// R-0178: Stop Waiting ends the wait, and no open is tried after it;
    /// Move To and a new open end it too.
    @Test func stopWaitingStopsTheOpens() async throws {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: Date(), answers: .cannotAsk)
        let c = BoxController(box: "acme-api", daemon: daemon)
        c.keepWaitingEvery = .milliseconds(20)
        await c.open()
        await c.choose(.keepWaiting)
        for _ in 0..<500 where daemon.calls.filter({ $0 == ["app", "open", "acme-api"] }).count < 2 {
            try await Task.sleep(for: .milliseconds(20))
        }
        c.stopWaiting()
        #expect(c.waitingFor == nil)
        let opens = daemon.calls.filter { $0 == ["app", "open", "acme-api"] }.count
        try await Task.sleep(for: .milliseconds(200))
        #expect(daemon.calls.filter { $0 == ["app", "open", "acme-api"] }.count == opens)

        await c.open() // refused again: the sheet
        await c.choose(.keepWaiting)
        #expect(c.waitingFor != nil)
        await c.requestMove(to: "ubuntu@test-server")
        #expect(c.waitingFor == nil)
    }

    /// R-0178: any other error ends the wait and is shown.
    @Test func anotherErrorEndsTheWait() async throws {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: Date(), answers: .cannotAsk)
        let c = BoxController(box: "acme-api", daemon: daemon)
        c.keepWaitingEvery = .milliseconds(20)
        await c.open()
        await c.choose(.keepWaiting)
        daemon.openElsewhere = nil
        daemon.failing["open"] = "test-server was removed from this Mac."
        for _ in 0..<500 where c.waitingFor != nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(c.waitingFor == nil)
        #expect(c.error == "test-server was removed from this Mac.")
    }

    /// R-0178: the place let go by the time GetPlacement answers (nobody
    /// holds it): the open is tried once more, and no sheet says a place
    /// isn't answering.
    @Test func whenNobodyHoldsItAnyMoreItOpens() async {
        let daemon = FakeDaemon()
        daemon.openElsewhere = Placement(holder: .none, newestSaveAt: nil, answers: nil)
        let c = BoxController(box: "acme-api", daemon: daemon)
        daemon.onPlacement = { _ in daemon.openElsewhere = nil }
        await c.open()
        #expect(c.leaseSheet == nil)
        #expect(daemon.calls.filter { $0 == ["app", "open", "acme-api"] }.count == 2)
        #expect(c.error == nil)
    }

    /// R-0178: Move To says it couldn't check, when the tabs can't be read.
    @Test func moveToWhenTheTabsCantBeRead() async {
        let daemon = FakeDaemon()
        daemon.failing["list-tab"] = "the workspace helper isn't answering"
        let c = BoxController(box: "acme-api", daemon: daemon)
        await c.run(.move("ubuntu@test-server"))
        #expect(c.moveConfirmation?.runningTitle == "Portenv couldn\u{2019}t check what\u{2019}s running. Anything still running will stop.")
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
        let drawn = Date(timeIntervalSince1970: 1_791_900_000)
        daemon.setTabs([TabInfo(id: "@0", name: "claude", active: true, program: "claude", command: "claude --continue",
                                runningSince: drawn.addingTimeInterval(-180))])
        let c = BoxController(box: "acme-api", daemon: daemon, now: { drawn })
        await c.run(.move("ubuntu@test-server"))
        #expect(c.moveConfirmation?.running == ["claude --continue in the claude tab \u{00B7} 3 minutes"])
        #expect(!daemon.calls.contains { $0.count > 1 && $0[1] == "move" })
        c.cancelMove()
        #expect(c.moveConfirmation == nil)
        await c.run(.move("ubuntu@test-server"))
        await c.confirmMove()
        #expect(daemon.calls.contains(["app", "move", "acme-api", "ubuntu@test-server"]))
    }
}
