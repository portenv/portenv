// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Records commands and answers like portenv app would.
final class FakeCLI: CLIRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [[String]] = []
    var failing: Set<String> = []
    var calls: [[String]] { lock.withLock { _calls } }

    func run(_ arguments: [String]) async throws -> String {
        lock.withLock { _calls.append(arguments) }
        let action = arguments.count > 1 ? arguments[1] : ""
        if failing.contains(action) { throw CLIError("there is no save point yet") }
        switch action {
        case "servers": return "ubuntu@server-a"
        case "open": return "open on mac (resume rule 3: start local)"
        default: return "ok"
        }
    }
}

@MainActor
struct BoxControllerTests {
    @Test func openLoadsServersAndAttaches() async {
        let cli = FakeCLI()
        let c = BoxController(box: "demo", cli: cli)
        await c.open()
        #expect(c.location == .thisMac)
        #expect(c.servers == ["ubuntu@server-a"])
        #expect(c.terminalGeneration == 1)
        #expect(cli.calls.contains(["app", "open", "demo"]))
    }

    @Test func moveToAServerAndBack() async {
        let cli = FakeCLI()
        let c = BoxController(box: "demo", cli: cli)
        await c.open()
        await c.move(to: "ubuntu@server-a")
        #expect(c.location == .server("ubuntu@server-a"))
        #expect(c.subtitle == "Open on ubuntu@server-a")
        await c.move(to: "this-mac")
        #expect(c.location == .thisMac)
        #expect(c.terminalGeneration == 2) // a new start: reattach
        #expect(cli.calls.contains(["app", "move", "demo", "this-mac"]))
    }

    @Test func revertWithoutASavePointShowsTheReasonAndKeepsTheSubtitle() async {
        let cli = FakeCLI()
        cli.failing = ["revert"]
        let c = BoxController(box: "demo", cli: cli)
        await c.open()
        let before = c.subtitle
        await c.revertToLastSavePoint()
        #expect(c.error == "there is no save point yet")
        #expect(c.subtitle == before)
        #expect(c.location == .thisMac)
    }

    @Test func revertAndSavePointNeedTheBoxOnThisMac() async {
        let cli = FakeCLI()
        let c = BoxController(box: "demo", cli: cli)
        await c.revertToLastSavePoint()
        await c.makeSavePoint()
        #expect(cli.calls.isEmpty)
    }

    @Test func closingSavesAndReleases() async {
        let cli = FakeCLI()
        let c = BoxController(box: "demo", cli: cli)
        await c.open()
        await c.close()
        #expect(cli.calls.last == ["app", "close", "demo"])
        #expect(c.location == .closed)
    }
}

@MainActor
struct AgentUnavailableTests {
    /// portenvd reports an agent channel that is down or fails verification
    /// (ADR 0010); the window shows that, and the box stays where it was.
    @Test func anUnavailableAgentIsReported() async {
        let cli = UnavailableCLI()
        let c = BoxController(box: "demo", cli: cli)
        await c.open()
        await c.makeSavePoint()
        #expect(c.error?.contains("the box agent is unavailable") == true)
        #expect(c.location == .thisMac)
    }
}

final class UnavailableCLI: CLIRunning, @unchecked Sendable {
    func run(_ arguments: [String]) async throws -> String {
        if arguments.count > 1, arguments[1] == "point" {
            throw CLIError("the box agent is unavailable (its channel is down or failed verification); restart the box")
        }
        return arguments.count > 1 && arguments[1] == "open" ? "open on mac (resume rule 3: start local)" : ""
    }
}
