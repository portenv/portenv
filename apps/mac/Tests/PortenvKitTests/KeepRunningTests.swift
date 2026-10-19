// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A workspace keeps running when its window closes (ADR 0023, R-0238). It
/// closes (saved and released) only on Close Workspace, on Quit, or on Move.
/// Save, release and lease are data-loss paths: these come first.
struct KeepRunningTests {
    static let running = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-16T13:17:00Z"}"#
    static let closed = #"{"state":"SAVE_STATE_CLOSED","saved_at":"2026-10-16T13:17:00Z"}"#
    static let onServer = #"{"state":"SAVE_STATE_SAVED","location":"dev@test-server"}"#

    func daemon(_ states: [String: String]) -> FakeDaemon {
        let d = FakeDaemon()
        d.boxNames = Array(states.keys).sorted()
        for (box, json) in states { d.setState(box, json) }
        return d
    }

    func closes(_ d: FakeDaemon) -> [String] {
        d.calls.filter { $0.count > 2 && ($0[1] == "close" || $0[1] == "leave-unsaved") }.map { $0[1] + " " + $0[2] }
    }

    /// Closing a workspace's last window asks portenvd nothing: no save, no
    /// release, no stop. The list replaces the window.
    @Test func closingTheLastWindowLeavesTheWorkspaceRunning() {
        #expect(WindowClose.action(showingBox: true, otherBoxWindows: 0) == .showListLeavingItRunning)
        #expect(WindowClose.action(showingBox: true, otherBoxWindows: 1) == .closeWindow)
        #expect(WindowClose.action(showingBox: false, otherBoxWindows: 0) == .closeWindow)
    }

    /// The list knows which workspaces run here: open on this Mac, not
    /// closed, not on a server.
    @Test func theListFindsTheRunningWorkspaces() async {
        let d = daemon(["acme-api": Self.running, "notes": Self.closed, "remote": Self.onServer, "test-119": Self.running])
        #expect(await RunningWorkspaces.here(d) == ["acme-api", "test-119"])
        #expect(closes(d).isEmpty)
    }

    /// Quit names the running workspaces, and closes (saves and releases)
    /// each once, in order, after the person says Quit.
    @Test func quitClosesEveryRunningWorkspaceOnce() async {
        let d = daemon(["acme-api": Self.running, "notes": Self.closed, "test-119": Self.running])
        let flow = await QuitAll.prepare(d)
        #expect(flow.boxes == ["acme-api", "test-119"])
        #expect(flow.confirmation == "acme-api and test-119 are running. Quitting saves and closes them.")
        let quit = await flow.run(ask: { _ in .cancel })
        #expect(quit)
        #expect(closes(d) == ["close acme-api", "close test-119"])
    }

    /// portenvd doesn't answer at Quit (R-0252): nothing can be saved, so the
    /// app asks first, naming the workspaces it knows are running, and never
    /// stops one. Quit then closes nothing: each keeps running for the next
    /// portenvd to take over.
    @Test func quitWithPortenvdDownAsksAndStopsNothing() async {
        let d = daemon(["acme-api": Self.running])
        d.failing["boxes"] = "portenvd is not running"
        let one = await QuitAll.prepare(d, known: ["test-119"])
        #expect(one.unreachable)
        #expect(one.confirmation == "Portenv's background service isn't running, so test-119 can't be saved now. It keeps running; quit anyway?")
        #expect(await one.run(ask: { _ in .cancel }))
        #expect(closes(d).isEmpty, "a workspace was closed or left unsaved while portenvd was down")
        let two = await QuitAll.prepare(d, known: ["test-119", "acme-api"])
        #expect(two.confirmation == "Portenv's background service isn't running, so acme-api and test-119 can't be saved now. They keep running; quit anyway?")
        // The app knows of none: it still asks, never quits silently.
        let none = await QuitAll.prepare(d, known: [])
        #expect(none.confirmation == "Portenv's background service isn't running, so no workspace can be saved now. Any that are running keep running; quit anyway?")
        #expect(closes(d).isEmpty)
    }

    /// Nothing running: no confirmation, nothing closed.
    @Test func quitWithNothingRunningAsksNothing() async {
        let d = daemon(["notes": Self.closed])
        let flow = await QuitAll.prepare(d)
        #expect(flow.confirmation == nil)
        #expect(await flow.run(ask: { _ in .cancel }))
        #expect(closes(d).isEmpty)
    }

    /// A workspace that can't be saved stops the quit on Cancel, with the
    /// workspaces before it already closed and the rest still running.
    @Test func cancelOnAFailedSaveStopsTheQuit() async {
        let d = daemon(["a": Self.running, "b": Self.running, "c": Self.running])
        d.failingFor = ["close b"]
        let flow = await QuitAll.prepare(d)
        let quit = await flow.run(ask: { _ in .cancel })
        #expect(!quit)
        #expect(closes(d) == ["close a", "close b"])
    }

    /// Quit Anyway on a failed save leaves that workspace with the quit
    /// marker (saved first thing on the next open) and goes on.
    @Test func quitAnywayLeavesTheMarkerAndGoesOn() async {
        let d = daemon(["a": Self.running, "b": Self.running])
        d.failingFor = ["close a"]
        let flow = await QuitAll.prepare(d)
        #expect(await flow.run(ask: { _ in .quitAnyway }))
        #expect(closes(d) == ["close a", "leave-unsaved a", "close b"])
    }

    /// One name, and three or more, read naturally.
    @Test func theConfirmationNamesThem() {
        #expect(QuitAll(daemon: FakeDaemon(), boxes: ["acme-api"]).confirmation == "acme-api is running. Quitting saves and closes it.")
        #expect(QuitAll(daemon: FakeDaemon(), boxes: ["a", "b", "c"]).confirmation == "a, b and c are running. Quitting saves and closes them.")
    }

    /// A Move To in progress: Quit and Close Workspace are refused plainly,
    /// never with Quit Anyway or Close Anyway, and nothing else is closed
    /// (R-0249).
    @Test func aMoveInProgressRefusesQuitAndCloseWorkspace() async {
        let d = daemon(["a": Self.running, "b": Self.running])
        d.movingFor = ["a"]
        let told = Told()
        let quit = await QuitAll.prepare(d).run(ask: { _ in
            Issue.record("asked Quit Anyway during a move")
            return .quitAnyway
        }, tell: { told.add($0) })
        #expect(!quit)
        #expect(told.all == ["a is moving to test-server. Close it once the move has finished."])
        #expect(closes(d) == ["close a"]) // refused; b is never touched, no leave-unsaved
        let closed = await WindowClose.run(QuitFlow(daemon: d, box: "a", openHere: true), ask: { _ in
            Issue.record("asked Close Anyway during a move")
            return .quitAnyway
        }, tell: { told.add($0) }) {}
        #expect(!closed)
        #expect(told.all.count == 2)
    }
}

final class Told: @unchecked Sendable {
    private let lock = NSLock()
    private var _all: [String] = []
    var all: [String] { lock.withLock { _all } }
    func add(_ s: String) { lock.withLock { _all.append(s) } }
}
