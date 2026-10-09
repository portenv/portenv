// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Answers the quit alert from a script and records what it was shown.
final class ScriptedAnswers: @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [QuitChoice]
    private(set) var shown: [String] = []
    init(_ answers: [QuitChoice]) { self.answers = answers }
    func ask(_ message: String) -> QuitChoice {
        lock.withLock {
            shown.append(message)
            return answers.isEmpty ? .cancel : answers.removeFirst()
        }
    }
}

/// Quitting never fails silently (PLAN.md 1.4).
struct QuitFlowTests {
    /// Relaunching for an update (ADR 0014): portenvd is told to leave the
    /// box running for the next one; nothing is closed, nothing is asked,
    /// even if portenvd doesn't answer.
    @Test func anUpdateRelaunchLeavesTheBoxRunning() async {
        let cli = FakeCLI()
        #expect(await QuitFlow(cli: cli, box: "acme-api", openHere: true).relaunch())
        let actions = cli.calls.map { $0.count > 1 ? $0[1] : "" }
        #expect(actions == ["relaunch"], "\(cli.calls)")
        cli.failing["relaunch"] = "portenvd is not running"
        #expect(await QuitFlow(cli: cli, box: "acme-api", openHere: true).relaunch(), "an update never waits on portenvd")
    }

    func closes(_ cli: FakeCLI) -> Int { cli.calls.filter { $0.count > 1 && $0[1] == "close" }.count }

    @Test func aSavedBoxQuitsWithoutAsking() async {
        let cli = FakeCLI()
        let answers = ScriptedAnswers([])
        let quit = await QuitFlow(cli: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(quit)
        #expect(answers.shown.isEmpty)
        #expect(closes(cli) == 1)
    }

    @Test func aFailedSaveAsksWithTheGuidelineText() async {
        let cli = FakeCLI()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.cancel])
        let quit = await QuitFlow(cli: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(!quit, "Cancel stays")
        #expect(answers.shown == ["acme-api couldn't be saved before quitting. Its work is still on this Mac."])
    }

    @Test func tryAgainClosesAgainThenQuits() async {
        let cli = FakeCLI()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.tryAgain, .tryAgain])
        let flow = QuitFlow(cli: cli, box: "acme-api", openHere: true)
        let task = Task { await flow.run { m in
            let a = answers.ask(m)
            if answers.shown.count == 2 { cli.failing = [:] } // the agent is back for the third try
            return a
        } }
        #expect(await task.value)
        #expect(closes(cli) == 3)
    }

    @Test func quitAnywayQuitsWithoutClosing() async {
        let cli = FakeCLI()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.quitAnyway])
        let quit = await QuitFlow(cli: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(quit)
        #expect(closes(cli) == 1)
    }

    @Test func aBoxNotOnThisMacNeedsNothing() async {
        let cli = FakeCLI()
        let quit = await QuitFlow(cli: cli, box: "acme-api", openHere: false).run { _ in .cancel }
        #expect(quit)
        #expect(cli.calls.isEmpty)
    }

    @Test func theStateLineSaysPortenvQuitBeforeSaving() {
        let at = ISO8601DateFormatter().date(from: "2026-10-08T13:54:00Z")!
        #expect(BoxState(save: .quitUnsaved, savedAt: at).line(timeZone: utc, locale: Locale(identifier: "en_GB")) == "This Mac · Not saved since 13:54 · Portenv quit before saving")
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_QUIT_UNSAVED","saved_at":"2026-10-08T13:54:00Z"}"#)?.save == .quitUnsaved)
    }
}
