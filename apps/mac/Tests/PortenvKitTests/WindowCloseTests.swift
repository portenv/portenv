// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Records, in order, what portenvd was asked and when the list was shown.
final class CloseOrder: @unchecked Sendable {
    private let lock = NSLock()
    private var _events: [String] = []
    var events: [String] { lock.withLock { _events } }
    func add(_ event: String) { lock.withLock { _events.append(event) } }
}

/// Closing the last box window goes back to "Your boxes" instead of quitting
/// (the owner, 2026-10-11). The box is closed, saved and released first,
/// exactly as quitting does; ⌘Q still quits.
struct WindowCloseTests {
    /// The data-loss path: the close (save and release) has finished before
    /// the list is shown.
    @Test func closingTheWindowSavesBeforeShowingTheList() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.onRun = { args in order.add(args.joined(separator: " ")) }
        let answers = ScriptedAnswers([])
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { answers.ask($0) }) {
            order.add("list")
        }
        #expect(shown)
        #expect(order.events == ["app close acme-api", "list"])
        #expect(answers.shown.isEmpty)
    }

    /// The window shows that the box is closing while it saves (R-0061):
    /// before the close, and still showing when the list replaces the box.
    @Test func theWindowShowsClosingWhileItSaves() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.onRun = { args in order.add(args.joined(separator: " ")) }
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { _ in .cancel },
                                          closing: { on in order.add(on ? "closing" : "closing off") }) {
            order.add("list")
        }
        #expect(shown)
        #expect(order.events == ["closing", "app close acme-api", "list"])
    }

    /// Staying in the box (Cancel) takes the closing line away again.
    @Test func cancelTakesTheClosingLineAway() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.failing["close"] = "the box agent is unavailable"
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { _ in .cancel },
                                          closing: { on in order.add(on ? "closing" : "closing off") }) {
            order.add("list")
        }
        #expect(!shown)
        #expect(order.events == ["closing", "closing off"])
    }

    /// The state line's closing row (GUIDELINES §3.1): the line, a spinning
    /// symbol, the Box menu disabled; the normal line again on staying.
    @MainActor
    @Test func theStateLineSaysClosing() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli)
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z"}"#
        await c.refresh()
        let normal = c.subtitle
        c.showClosing(true)
        #expect(c.subtitle == "Saving before closing…")
        #expect(c.title.spins && c.title.consistent)
        #expect(c.busy)
        c.showClosing(false)
        #expect(c.subtitle == normal)
        #expect(!c.busy && c.title.consistent)
    }

    /// A failed save asks, in closing's words; Cancel keeps the box open in
    /// its window and never shows the list.
    @Test func aFailedSaveAsksAndCancelStaysInTheBox() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.failing["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.cancel])
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { answers.ask($0) }) {
            order.add("list")
        }
        #expect(!shown)
        #expect(order.events.isEmpty)
        #expect(answers.shown == ["acme-api couldn't be saved before closing. Its work is still on this Mac."])
    }

    /// Try Again closes again; once saved, the list.
    @Test func tryAgainThenTheList() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.failingOnce["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([.tryAgain])
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { answers.ask($0) }) {
            order.add("list")
        }
        #expect(shown)
        #expect(cli.calls.filter { $0[1] == "close" }.count == 2)
        #expect(order.events == ["list"])
    }

    /// Close Anyway leaves the box unsaved with the marker, as Quit Anyway
    /// does, so the next open saves it first thing; then the list.
    @Test func closeAnywayLeavesTheMarkerThenTheList() async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.failing["close"] = "the box agent is unavailable"
        cli.onRun = { args in order.add(args.joined(separator: " ")) }
        let answers = ScriptedAnswers([.quitAnyway])
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { answers.ask($0) }) {
            order.add("list")
        }
        #expect(shown)
        #expect(order.events == ["app close acme-api", "app leave-unsaved acme-api", "list"])
    }

    /// R-0067: through a failed save, the closing line stays as it is. Try
    /// Again re-uses it (one "closing", never off), and Close Anyway goes
    /// straight to the list. The list drops the box's controller
    /// (AppModel.showList), so the line can't outlive the box.
    @Test(arguments: [QuitChoice.tryAgain, .quitAnyway])
    func aFailedSaveKeepsOneClosingLineUntilTheList(_ choice: QuitChoice) async {
        let cli = FakeDaemon(), order = CloseOrder()
        cli.failingOnce["close"] = "the box agent is unavailable"
        let answers = ScriptedAnswers([choice])
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: true), ask: { answers.ask($0) },
                                          closing: { on in order.add(on ? "closing" : "closing off") }) {
            order.add("list")
        }
        #expect(shown)
        #expect(order.events == ["closing", "list"])
    }

    /// A box that isn't open on this Mac needs nothing here.
    @Test func aBoxNotOnThisMacGoesStraightToTheList() async {
        let cli = FakeDaemon(), order = CloseOrder()
        let shown = await WindowClose.run(QuitFlow(daemon: cli, box: "acme-api", openHere: false), ask: { _ in .cancel }) {
            order.add("list")
        }
        #expect(shown)
        #expect(cli.calls.isEmpty)
        #expect(order.events == ["list"])
    }

    /// Only the last window showing a box closes it; any other window (the
    /// list, first run, or a second window on the same box) just closes.
    @Test func onlyTheLastBoxWindowClosesTheBox() {
        #expect(WindowClose.action(showingBox: true, otherBoxWindows: 0) == .closeBoxThenShowList)
        #expect(WindowClose.action(showingBox: true, otherBoxWindows: 1) == .closeWindow)
        #expect(WindowClose.action(showingBox: false, otherBoxWindows: 0) == .closeWindow)
    }

    /// File › Close acts on the key window, else the main window, else the
    /// frontmost one: with Portenv behind there's no key window (R-0061).
    @Test func fileCloseFindsItsWindowWithTheAppBehind() {
        final class Win {}
        let key = Win(), main = Win(), front = Win(), back = Win()
        #expect(WindowClose.target(key: key, main: main, frontToBack: [front, back]) === key)
        #expect(WindowClose.target(key: nil, main: main, frontToBack: [front, back]) === main)
        #expect(WindowClose.target(key: nil, main: nil, frontToBack: [front, back]) === front)
        #expect(WindowClose.target(key: nil, main: nil, frontToBack: [Win]()) == nil)
    }

    /// The alert's words for closing, and quitting's unchanged.
    @Test func theAlertSaysClosingNotQuitting() {
        #expect(WindowClose.message(box: "acme-api") == "acme-api couldn't be saved before closing. Its work is still on this Mac.")
        #expect(WindowClose.anywayButton == "Close Anyway")
        #expect(QuitFlow.message(box: "acme-api") == "acme-api couldn't be saved before quitting. Its work is still on this Mac.")
    }
}
