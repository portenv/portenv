// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The three sheets of the move-and-lease board (2.3; R-0136, R-0140,
/// R-0168): their words, their options and which one Return presses.
struct LeaseSheetTests {
    private let now = Date(timeIntervalSince1970: 1_791_900_000)
    private func ago(_ minutes: Double) -> Date { now.addingTimeInterval(-minutes * 60) }
    private let server = Placement.Holder.knownServer(id: "srv-0123456789ab", name: "test-server")

    /// A known server that answers: Connect to it there is the default,
    /// Move it to this Mac second, and Look is hidden.
    @Test func aServerThatAnswersOffersConnect() {
        let s = LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: ago(3), answers: .yes), now: now)
        #expect(s.kind == .openThere)
        #expect(s.title == "acme-api is open on test-server")
        #expect(s.choices.map(\.action) == [.connect, .moveHere])
        #expect(s.defaultAction == .connect)
        #expect(!s.choices.map(\.title).joined().contains("Look"))
    }

    /// While the probe runs, the sheet already names the place and says
    /// it's checking; no choice can be made yet but Cancel.
    @Test func checkingNamesThePlace() {
        let s = LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: ago(3), answers: nil), now: now)
        #expect(s.kind == .checking)
        #expect(s.detail == "Checking whether test-server answers…")
        #expect(s.choices.isEmpty)
    }

    /// R-0168: a known server that failed its ping defaults to Move to This
    /// Mac only when its newest save is under an hour old; otherwise Keep
    /// Waiting.
    @Test(arguments: [(30.0, LeaseSheet.Action.moveHereTakingOver), (59.0, .moveHereTakingOver), (61.0, .keepWaiting), (600.0, .keepWaiting)])
    func aServerThatDoesntAnswerFollowsTheHourRule(minutes: Double, want: LeaseSheet.Action) {
        let s = LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: ago(minutes), answers: .no), now: now)
        #expect(s.kind == .notAnswering)
        #expect(s.title == "test-server isn\u{2019}t answering")
        #expect(s.choices.map(\.action) == [.keepWaiting, .moveHereTakingOver])
        #expect(s.defaultAction == want)
    }

    /// R-0168: a holder Portenv can't ask never makes Move the default:
    /// Keep Waiting is, and Move to This Mac carries its consequence line.
    @Test(arguments: [1.0, 30.0, 600.0])
    func aHolderThatCantBeAskedDefaultsToKeepWaiting(minutes: Double) {
        let s = LeaseSheet(box: "acme-api", placement: Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: ago(minutes), answers: .cannotAsk), now: now)
        #expect(s.defaultAction == .keepWaiting)
        #expect(s.choices.map(\.action) == [.keepWaiting, .moveHereTakingOver])
        #expect(s.choices.last?.detail == "Another Mac may still be working in it. Anything it hasn\u{2019}t saved yet stays there, and is kept as a separate save when it next saves.")
        // The box says only where it starts from: the line under the
        // buttons says the rest (R-0178).
        #expect(s.warning?.text.hasPrefix("It starts from") == true)
        #expect(s.warning.map { !$0.text.contains("separate save") } == true)
    }

    /// The newest save's time and age are said, and the warning box says
    /// later work isn't lost (resume rule 4).
    @Test func theStaleSheetSaysTheNewestSaveAndRule4() {
        let saved = ago(26)
        let s = LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: saved, answers: .no), now: now,
                           clock: { _ in "14:58" })
        #expect(s.body == "acme-api was last open there. Its newest save on test-server is from 14:58, 26 minutes ago.")
        #expect(s.warning?.title == "If you move it to this Mac now:")
        #expect(s.warning?.text == "It starts from the 14:58 save. Anything done on test-server after that isn\u{2019}t lost: when test-server comes back, Portenv keeps it as a separate save you can open.")
    }

    /// The Move confirmation names every running command, its tab and how
    /// long it has run, or says nothing is running; Move is the default.
    @Test func theMoveConfirmation() {
        let tabs = [TabInfo(id: "@0", name: "claude", active: true, program: "claude", command: "claude --continue", runningSince: ago(3)),
                    TabInfo(id: "@1", name: "shell", active: false)]
        let m = MoveConfirmation(box: "acme-api", target: "ubuntu@test-server", tabs: tabs, now: now)
        #expect(m.title == "Move acme-api to test-server?")
        #expect(m.running == ["claude --continue in the claude tab \u{00B7} 3 minutes"])
        let long = MoveConfirmation(box: "acme-api", target: "ubuntu@test-server",
                                    tabs: [TabInfo(id: "@2", name: "build", active: false, command: "make", runningSince: ago(120))], now: now)
        #expect(long.running == ["make in the build tab \u{00B7} 2 hours"])
        // tabs couldn't be read: never "Nothing is running." (R-0178)
        let unknown = MoveConfirmation(box: "acme-api", target: "ubuntu@test-server", tabs: nil, now: now)
        #expect(unknown.runningTitle == "Portenv couldn\u{2019}t check what\u{2019}s running. Anything still running will stop.")
        #expect(unknown.running.isEmpty)
        #expect(m.runningTitle == "1 command is still running and will stop:")
        let idle = MoveConfirmation(box: "acme-api", target: "this-mac", tabs: [tabs[1]], now: now)
        #expect(idle.running.isEmpty)
        #expect(idle.runningTitle == "Nothing is running.")
        #expect(idle.title == "Move acme-api to This Mac?")
        #expect(MoveConfirmation.footer == "Tabs, files and saved commands come along. Agents connected here are disconnected and can reconnect there.")
    }

    /// Servers show by name, never by address (GUIDELINES §2), and no
    /// sheet says "lease".
    @Test func noAddressNoLease() {
        var all: [String] = []
        for (placement, minutes) in [(Placement(holder: server, newestSaveAt: ago(3), answers: .yes), 3.0),
                                     (Placement(holder: server, newestSaveAt: ago(26), answers: .no), 26),
                                     (Placement(holder: server, newestSaveAt: nil, answers: nil), 0),
                                     (Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: ago(90), answers: .cannotAsk), 90)] {
            _ = minutes
            let s = LeaseSheet(box: "acme-api", placement: placement, now: now)
            all += [s.title, s.body, s.detail ?? "", s.warning?.title ?? "", s.warning?.text ?? "", s.footnote ?? ""]
            all += s.choices.flatMap { [$0.title, $0.detail ?? "", $0.button] }
        }
        let m = MoveConfirmation(box: "acme-api", target: "ubuntu@203.0.113.24", tabs: [], now: now)
        all += [m.title, m.body, m.runningTitle, MoveConfirmation.footer]
        for line in all {
            #expect(!line.lowercased().contains("lease"), "\(line)")
            #expect(!line.contains("ubuntu@"), "\(line)")
        }
    }
}
