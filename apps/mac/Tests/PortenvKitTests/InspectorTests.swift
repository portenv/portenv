// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The inspector (GUIDELINES.md §6) and the notifications (§5), 1.2 items 3
/// and 4.
@MainActor
struct InspectorTests {
    private let noon = Date(timeIntervalSince1970: 1_791_288_000) // a fixed time

    // MARK: Where it is

    @Test func whereItIsSaysWhereTheBoxRunsAndWhereSavesAreEncrypted() {
        #expect(InspectorText.runningOn(nil) == "Running on This Mac")
        #expect(InspectorText.runningOn("test-server") == "Running on test-server")
        #expect(InspectorText.encryption(nil) == "Encrypted on this Mac before they leave it")
        #expect(InspectorText.encryption("test-server") == "Encrypted on test-server before they leave it")
    }

    @Test func packagesThatCouldNotBeInstalledAreNamed() {
        #expect(InspectorText.packagesLine([]) == nil)
        #expect(InspectorText.packagesLine(["ripgrep"]) == "1 package couldn't be installed: ripgrep")
        #expect(InspectorText.packagesLine(["ripgrep", "fd-find"]) == "2 packages couldn't be installed: ripgrep, fd-find")
    }

    @Test func duringAMoveTheSectionSaysWhatStillWorks() {
        #expect(InspectorText.moving(to: "test-server") == "Moving to test-server")
        #expect(InspectorText.moveNote
            == "You can keep reading. Typing comes back when the box is running there. If the move stops, the box stays here, saved.")
    }

    // MARK: Saves

    @Test func savesShowTheFiveNewestFirstWithTheirKind() {
        let saves = (0..<7).map { i in
            SaveInfo(id: "s\(i)", time: noon.addingTimeInterval(Double(i) * 60), kind: i == 3 ? "point" : "autosave", machine: "mac")
        }
        let rows = InspectorText.saveRows(saves)
        #expect(rows.count == 5)
        #expect(rows.first?.id == "s6") // newest first
        #expect(rows.map(\.id) == ["s6", "s5", "s4", "s3", "s2"])
        #expect(rows.first { $0.id == "s3" }?.kind == "Save point")
        #expect(rows.first { $0.id == "s6" }?.kind == "Autosave")
    }

    @Test func everySaveKindPortenvdReportsHasAPlainLabel() {
        #expect(InspectorText.kind("autosave", machine: "mac") == "Autosave")
        #expect(InspectorText.kind("point", machine: "mac") == "Save point")
        #expect(InspectorText.kind("release", machine: "mac") == "Saved when the box closed")
        #expect(InspectorText.kind("orphaned", machine: "studio") == "Kept from studio")
        // Something newer than this app: shown as a save, never blank.
        #expect(InspectorText.kind("future-kind", machine: "mac") == "Save")
    }

    @Test func aSavePointsNameIsKeptForItsSecondLine() {
        let saves = [SaveInfo(id: "p", time: noon, kind: "point", machine: "mac", name: "before retry change"),
                     SaveInfo(id: "a", time: noon.addingTimeInterval(60), kind: "autosave", machine: "mac")]
        let rows = InspectorText.saveRows(saves)
        #expect(rows.first { $0.id == "p" }?.name == "before retry change")
        #expect(rows.first { $0.id == "a" }?.name == nil)
        // A name given as only spaces isn't shown.
        #expect(InspectorText.saveRows([SaveInfo(id: "x", time: noon, kind: "point", machine: "mac", name: "  ")]).first?.name == nil)
    }

    @Test func onlyTheNewestSavePointOffersRevert() {
        // Revert To ▸ Last Save Point is the revert that exists (1.0); other
        // saves come with Browse Saves (1.4).
        let saves = [
            SaveInfo(id: "a", time: noon, kind: "point", machine: "mac"),
            SaveInfo(id: "b", time: noon.addingTimeInterval(60), kind: "autosave", machine: "mac"),
            SaveInfo(id: "c", time: noon.addingTimeInterval(120), kind: "point", machine: "mac"),
        ]
        let rows = InspectorText.saveRows(saves)
        #expect(rows.filter(\.revertable).map(\.id) == ["c"])
    }

    @Test func aBoxOnAServerListsItsSavesThere() {
        #expect(InspectorText.savesElsewhere("test-server") == "Saves are listed on test-server while the box runs there.")
        #expect(InspectorText.noSaves == "No saves yet.")
    }

    // MARK: Who's here and Running now

    @Test func whosHereIsYouUntilAgentsArrive() {
        #expect(InspectorText.you == "You")
        #expect(InspectorText.noAgents == "No agents connected.")
    }

    @Test func runningNowListsEachTabAndWhatRunsInIt() {
        let tabs = [
            TabInfo(id: "@0", name: "claude", active: true, program: "claude"),
            TabInfo(id: "@1", name: "shell", active: false),
        ]
        // Only a real program name is a program; a shell is "idle", in
        // plain secondary text (R-0019).
        #expect(InspectorText.tabRows(tabs) == [
            InspectorText.TabRow(id: "@0", name: "claude", program: "claude"),
            InspectorText.TabRow(id: "@1", name: "shell", program: nil),
        ])
        #expect(InspectorText.idle == "idle")
    }

    // MARK: Notifications (§5)

    @Test func aCommandThatEndsIsFoundWithHowLongItRan() {
        var watch = CommandWatch()
        let t0 = noon
        #expect(watch.update([TabInfo(id: "@0", name: "build", active: true)], at: t0).isEmpty)
        #expect(watch.update([TabInfo(id: "@0", name: "build", active: true, program: "make")], at: t0).isEmpty)
        // Still running in the next update: nothing finished, and the start
        // time stays the first one.
        #expect(watch.update([TabInfo(id: "@0", name: "build", active: true, program: "make")], at: t0.addingTimeInterval(30)).isEmpty)
        let done = watch.update([TabInfo(id: "@0", name: "build", active: true)], at: t0.addingTimeInterval(95))
        #expect(done == [CommandWatch.Finished(tab: "build", program: "make", seconds: 95)])
        // Reported once.
        #expect(watch.update([TabInfo(id: "@0", name: "build", active: true)], at: t0.addingTimeInterval(100)).isEmpty)
    }

    @Test func aProgramReplacedByAnotherCountsAsFinished() {
        var watch = CommandWatch()
        _ = watch.update([TabInfo(id: "@0", name: "t", active: true, program: "npm")], at: noon)
        let done = watch.update([TabInfo(id: "@0", name: "t", active: true, program: "node")], at: noon.addingTimeInterval(40))
        #expect(done == [CommandWatch.Finished(tab: "t", program: "npm", seconds: 40)])
    }

    @Test func aClosedTabIsNotACommandThatFinished() {
        var watch = CommandWatch()
        _ = watch.update([TabInfo(id: "@0", name: "t", active: true, program: "npm")], at: noon)
        #expect(watch.update([], at: noon.addingTimeInterval(60)).isEmpty)
    }

    @Test func notificationsOnlyWhenTheWindowIsNotInFrontAndPastTheThreshold() {
        // A command: longer than 30 s.
        #expect(NotifyRules.commandFinished(seconds: 31, windowInFront: false))
        #expect(!NotifyRules.commandFinished(seconds: 30, windowInFront: false))
        #expect(!NotifyRules.commandFinished(seconds: 300, windowInFront: true))
        // A move: longer than 10 s.
        #expect(NotifyRules.moveFinished(seconds: 11, windowInFront: false))
        #expect(!NotifyRules.moveFinished(seconds: 10, windowInFront: false))
        #expect(!NotifyRules.moveFinished(seconds: 60, windowInFront: true))
    }

    @Test func notificationTextsFollowTheTitleRule() {
        // Title: "<box> · <event>"; body one line; subtitle who and where.
        let c = NotifyText.commandFinished(box: "acme-api", tab: "build", program: "make", seconds: 95, location: nil)
        #expect(c.title == "acme-api · make finished")
        #expect(c.body == "In the build tab, after 1 min 35 s.")
        #expect(c.subtitle == "You · This Mac")
        let m = NotifyText.moveFinished(box: "acme-api", to: "test-server", seconds: 42)
        #expect(m.title == "acme-api · Moved to test-server")
        #expect(m.body == "The move took 42 s. The box is running there.")
        #expect(NotifyText.duration(9) == "9 s")
        #expect(NotifyText.duration(60) == "1 min")
        #expect(NotifyText.duration(3725) == "1 h 2 min")
    }

    @Test func followingTheTabsReportsEachFinishedCommand() async {
        let d = FakeDaemon()
        d.setTabs([TabInfo(id: "@0", name: "build", active: true)])
        let clock = StepClock(steps: [noon, noon, noon.addingTimeInterval(50)])
        let c = TabsController(box: "acme-api", daemon: d, now: { clock.next() })
        var finished: [CommandWatch.Finished] = []
        c.commandFinished = { finished.append($0) }
        let following = Task { await c.follow() }
        try? await Task.sleep(for: .milliseconds(100))
        d.pushTabs([TabInfo(id: "@0", name: "build", active: true, program: "make")])
        try? await Task.sleep(for: .milliseconds(100))
        d.pushTabs([TabInfo(id: "@0", name: "build", active: true)])
        try? await Task.sleep(for: .milliseconds(100))
        following.cancel()
        #expect(finished == [CommandWatch.Finished(tab: "build", program: "make", seconds: 50)])
    }

    @Test func theStateCarriesWhereSavesGoAndWhenTheBoxOpened() {
        let s = BoxState.parse(#"{"state":"SAVE_STATE_SAVED","storage_place":"test-server.example","opened_at":"2026-10-10T09:12:00Z"}"#)
        #expect(s?.storagePlace == "test-server.example") // as portenvd sends it
        #expect(s?.openedAt == ISO8601DateFormatter().date(from: "2026-10-10T09:12:00Z"))
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_SAVED","storage_place":""}"#)?.storagePlace == nil)
        #expect(InspectorText.savesGoTo("test-server.example") == "Saves go to test-server.example")
        #expect(InspectorText.since(ISO8601DateFormatter().date(from: "2026-10-10T09:12:00Z")!, calendar: .utc) == "Since 09:12")
    }

    @Test func theContentIsBuiltFromTheWindowsControllers() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-10T09:30:00Z","storage_place":"test-server.example","opened_at":"2026-10-10T09:12:00Z","failed_packages":["ripgrep"]}"#
        d.saves = [SaveInfo(id: "a", time: noon, kind: "autosave", machine: "mac")]
        d.setTabs([TabInfo(id: "@0", name: "claude", active: true, program: "claude")])
        let box = BoxController(box: "acme-api", daemon: d)
        await box.refresh()
        await box.refreshSaves()
        let tabs = TabsController(box: "acme-api", daemon: d)
        await tabs.load()
        let c = InspectorContent(box: box, tabs: tabs)
        #expect(c.place == nil) // This Mac
        #expect(c.movingTo == nil)
        #expect(c.storagePlace == "test-server.example")
        #expect(c.openedAt != nil)
        #expect(c.failedPackages == ["ripgrep"])
        #expect(c.saves.map(\.id) == ["a"])
        #expect(c.savesElsewhere == nil)
        #expect(c.tabs.map(\.name) == ["claude"])
    }

    @Test func everyInspectorControlHasAVoiceOverLabel() {
        #expect(A11y.inspector == "Inspector")
        #expect(A11y.showInspector == "Show inspector")
        #expect(A11y.hideInspector == "Hide inspector")
        #expect(A11y.retryPackages == "Retry installing packages")
        #expect(A11y.revertTo("Save point") == "Revert to this Save point")
        #expect(A11y.runningTab("claude", program: "claude") == "claude tab, running claude")
        #expect(A11y.runningTab("shell", program: nil) == "shell tab, idle")
    }

    // MARK: BoxController

    @Test func savesAreReadForABoxOpenHereAndNotForOneOnAServer() async {
        let d = FakeDaemon()
        d.saves = [SaveInfo(id: "x", time: noon, kind: "point", machine: "mac")]
        d.state = #"{"state":"SAVE_STATE_SAVED"}"#
        let c = BoxController(box: "acme-api", daemon: d)
        await c.refresh()
        await c.refreshSaves()
        #expect(c.saves.map(\.id) == ["x"])
        #expect(d.calls.contains(["app", "saves", "acme-api"]))

        let s = FakeDaemon()
        s.state = #"{"state":"SAVE_STATE_SAVED","location":"test-server"}"#
        let cs = BoxController(box: "acme-api", daemon: s)
        await cs.refresh()
        await cs.refreshSaves()
        #expect(cs.saves.isEmpty)
        #expect(!s.calls.contains(["app", "saves", "acme-api"]))
    }

    @Test func retryAsksPortenvdToInstallThePackagesAgain() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED","failed_packages":["ripgrep"]}"#
        let c = BoxController(box: "acme-api", daemon: d)
        await c.refresh()
        await c.retryPackages()
        #expect(d.calls.contains(["app", "retry-packages", "acme-api"]))
    }

    @Test func aLongMoveIsReportedToTheNotifierWithItsDuration() async {
        let d = FakeDaemon()
        d.state = #"{"state":"SAVE_STATE_SAVED"}"#
        let n = RecordingNotifier()
        let clock = StepClock(steps: [noon, noon.addingTimeInterval(42)])
        let c = BoxController(box: "acme-api", daemon: d, notifier: n, now: { clock.next() })
        await c.refresh()
        await c.move(to: "test-server")
        #expect(n.moves == [RecordingNotifier.Move(box: "acme-api", to: "test-server", seconds: 42)])
    }
}

/// A notifier that records what it was told.
final class RecordingNotifier: KeychainWaitNotifying, @unchecked Sendable {
    struct Move: Equatable { let box: String; let to: String; let seconds: Int }
    private let lock = NSLock()
    private var _moves: [Move] = []
    var moves: [Move] { lock.withLock { _moves } }
    func keychainWaiting(box: String) async {}
    func savedAfterQuit(box: String, at: Date) async {}
    func moveFinished(box: String, to: String, seconds: Int) async {
        lock.withLock { _moves.append(Move(box: box, to: to, seconds: seconds)) }
    }
}

/// Hands out the given times in turn, then repeats the last.
final class StepClock: @unchecked Sendable {
    private let lock = NSLock()
    private var steps: [Date]
    init(steps: [Date]) { self.steps = steps }
    func next() -> Date { lock.withLock { steps.count > 1 ? steps.removeFirst() : steps[0] } }
}

extension Calendar {
    /// A Gregorian calendar in UTC, so times format the same on every Mac.
    static var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        c.locale = Locale(identifier: "en_GB")
        return c
    }
}
