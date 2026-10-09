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
        let cli = FakeDaemon()
        #expect(await QuitFlow(daemon: cli, box: "acme-api", openHere: true).relaunch())
        let actions = cli.calls.map { $0.count > 1 ? $0[1] : "" }
        #expect(actions == ["relaunch"], "\(cli.calls)")
        cli.failing["relaunch"] = "portenvd is not running"
        #expect(await QuitFlow(daemon: cli, box: "acme-api", openHere: true).relaunch(), "an update never waits on portenvd")
    }

    /// Quit Anyway (1.1: portenvd, run by launchd, outlives the app): the
    /// box is left unsaved with the quit marker, so the next open saves it
    /// first thing; the app quits even if portenvd doesn't answer.
    @Test func quitAnywayLeavesTheBoxUnsavedWithTheMarker() async {
        let cli = FakeDaemon()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.quitAnyway])
        #expect(await QuitFlow(daemon: cli, box: "acme-api", openHere: true).run { answers.ask($0) })
        #expect(cli.calls.last == ["app", "leave-unsaved", "acme-api"], "\(cli.calls)")
        cli.failing["leave-unsaved"] = "portenvd is not running"
        let again = ScriptedAnswers([.quitAnyway])
        #expect(await QuitFlow(daemon: cli, box: "acme-api", openHere: true).run { again.ask($0) })
    }

    func closes(_ cli: FakeDaemon) -> Int { cli.calls.filter { $0.count > 1 && $0[1] == "close" }.count }

    @Test func aSavedBoxQuitsWithoutAsking() async {
        let cli = FakeDaemon()
        let answers = ScriptedAnswers([])
        let quit = await QuitFlow(daemon: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(quit)
        #expect(answers.shown.isEmpty)
        #expect(closes(cli) == 1)
    }

    @Test func aFailedSaveAsksWithTheGuidelineText() async {
        let cli = FakeDaemon()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.cancel])
        let quit = await QuitFlow(daemon: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(!quit, "Cancel stays")
        #expect(answers.shown == ["acme-api couldn't be saved before quitting. Its work is still on this Mac."])
    }

    @Test func tryAgainClosesAgainThenQuits() async {
        let cli = FakeDaemon()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.tryAgain, .tryAgain])
        let flow = QuitFlow(daemon: cli, box: "acme-api", openHere: true)
        let task = Task { await flow.run { m in
            let a = answers.ask(m)
            if answers.shown.count == 2 { cli.failing = [:] } // the agent is back for the third try
            return a
        } }
        #expect(await task.value)
        #expect(closes(cli) == 3)
    }

    @Test func quitAnywayQuitsWithoutClosing() async {
        let cli = FakeDaemon()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.quitAnyway])
        let quit = await QuitFlow(daemon: cli, box: "acme-api", openHere: true).run { answers.ask($0) }
        #expect(quit)
        #expect(closes(cli) == 1)
    }

    @Test func aBoxNotOnThisMacNeedsNothing() async {
        let cli = FakeDaemon()
        let quit = await QuitFlow(daemon: cli, box: "acme-api", openHere: false).run { _ in .cancel }
        #expect(quit)
        #expect(cli.calls.isEmpty)
    }

    @Test func theStateLineSaysPortenvQuitBeforeSaving() {
        let at = ISO8601DateFormatter().date(from: "2026-10-08T13:54:00Z")!
        #expect(BoxState(save: .quitUnsaved, savedAt: at).line(timeZone: utc, locale: Locale(identifier: "en_GB")) == "This Mac · Not saved since 13:54 · Portenv quit before saving")
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_QUIT_UNSAVED","saved_at":"2026-10-08T13:54:00Z"}"#)?.save == .quitUnsaved)
    }

    /// A box saved in the background after Quit Anyway: one notification at
    /// the next launch, in the guideline's words; given out once.
    @MainActor
    @Test func aBackgroundSaveIsNotifiedOnceAtLaunch() async {
        let cli = FakeDaemon(), n = CountingNotifier()
        cli.savedAfterQuit = [SavedAfterQuit(box: "acme-api", savedAt: Date())]
        let c = BoxController(box: "acme-api", daemon: cli, notifier: n)
        await c.announceBackgroundSaves()
        await c.announceBackgroundSaves()
        #expect(n.saved == ["acme-api"])
        let at = ISO8601DateFormatter().date(from: "2026-10-09T13:02:00Z")!
        #expect(SavedAfterQuitNote.title(box: "acme-api") == "acme-api · Saved after Portenv quit")
        #expect(SavedAfterQuitNote.body(at: at, timeZone: TimeZone(identifier: "Europe/Paris")!, locale: Locale(identifier: "en_GB"))
            == "Saved at 15:02 and closed. Nothing was lost.")
    }
}
