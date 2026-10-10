// SPDX-License-Identifier: Apache-2.0

import Testing

@testable import PortenvKit

/// What the app shows when it starts (PLAN.md 1.5, "No window for a box that
/// doesn't exist"): first run when there's no box, the box list when the box
/// asked for isn't here, and never a window for a box that doesn't exist.
struct LaunchTests {
    @Test func noBoxAtAllIsFirstRun() {
        #expect(LaunchDecision.decide(requested: nil, boxes: [], lastBox: nil, firstRunDone: false) == .firstRun(.welcome))
        // Opened with a name, but nothing exists: still first run, no window
        // for the missing box (found at the Mac: "demo").
        #expect(LaunchDecision.decide(requested: "demo", boxes: [], lastBox: nil, firstRunDone: false) == .firstRun(.welcome))
    }

    /// Someone who finished first run and later removed every box gets the
    /// first-box step, not the whole welcome again.
    @Test func noBoxAfterFirstRunStartsAtTheFirstBox() {
        #expect(LaunchDecision.decide(requested: nil, boxes: [], lastBox: "acme-api", firstRunDone: true) == .firstRun(.firstBox))
    }

    @Test func aRequestedBoxThatExistsOpens() {
        #expect(LaunchDecision.decide(requested: "acme-api", boxes: ["acme-api", "site"], lastBox: "site", firstRunDone: true) == .open("acme-api"))
    }

    /// A box that isn't here never gets a window: the box list says so, in a
    /// sentence that says what to do next (GUIDELINES §10).
    @Test func aRequestedBoxThatIsMissingShowsTheList() {
        let launch = LaunchDecision.decide(requested: "demo", boxes: ["acme-api"], lastBox: nil, firstRunDone: true)
        #expect(launch == .boxList(notice: "There\u{2019}s no box called “demo” on this Mac. Choose one of your boxes, or make a new one."))
        if case .open = launch { Issue.record("a missing box must never open a window") }
    }

    @Test func withoutARequestTheLastBoxReopens() {
        #expect(LaunchDecision.decide(requested: nil, boxes: ["acme-api", "site"], lastBox: "site", firstRunDone: true) == .open("site"))
    }

    /// The last box was removed meanwhile: never reopen it, show the list.
    @Test func aLastBoxThatIsGoneShowsTheList() {
        #expect(LaunchDecision.decide(requested: nil, boxes: ["acme-api", "site"], lastBox: "old", firstRunDone: true) == .boxList(notice: nil))
    }

    @Test func aSingleBoxOpensWithoutAList() {
        #expect(LaunchDecision.decide(requested: nil, boxes: ["acme-api"], lastBox: nil, firstRunDone: false) == .open("acme-api"))
    }

    @Test func severalBoxesAndNoLastBoxShowTheList() {
        #expect(LaunchDecision.decide(requested: nil, boxes: ["acme-api", "site"], lastBox: nil, firstRunDone: true) == .boxList(notice: nil))
    }

    /// An empty PORTENV_BOX counts as no request.
    @Test func anEmptyRequestIsNoRequest() {
        #expect(LaunchDecision.decide(requested: "", boxes: ["acme-api"], lastBox: nil, firstRunDone: true) == .open("acme-api"))
    }
}
