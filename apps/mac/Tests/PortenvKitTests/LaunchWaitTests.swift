// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The launch's wait for portenvd (R-0121): never a spinner without end.
/// Two missed checks in a row (about 4 s) show the service notice, and the
/// checks go on until portenvd answers.
@MainActor
struct LaunchWaitTests {
    /// Records what the wait reported, in order.
    final class Reports {
        var all: [LaunchWait.Report] = []
    }

    /// An answer straight away: nothing reported, no notice.
    @Test func anAnswerEndsTheWait() async {
        let reports = Reports()
        let outcome = await LaunchWait.untilAnswered(
            ping: { true }, problem: { nil }, report: { reports.all.append($0) }, sleep: { _ in })
        #expect(outcome == .answered)
        #expect(reports.all.isEmpty)
    }

    /// One missed check (2 s) says nothing; the second in a row says the
    /// service isn't answering, once; then an answer ends the wait.
    @Test func twoMissesSayTheServiceIsntAnswering() async {
        let reports = Reports()
        var pings = 0
        var slept: Duration = .zero
        let answerAt = 3 * LaunchWait.pingsPerCheck + 1
        let outcome = await LaunchWait.untilAnswered(
            ping: { pings += 1; return pings == answerAt },
            problem: { nil },
            report: { r in
                reports.all.append(r)
                #expect(slept >= .seconds(4)) // not before two whole checks
            },
            sleep: { slept += $0 })
        #expect(outcome == .answered)
        #expect(reports.all == [.notAnswering]) // once, though three checks missed
        #expect(LaunchWait.pollEvery * LaunchWait.pingsPerCheck == .seconds(2))
    }

    /// The login item waiting for approval is said as such at the first
    /// missed check, and the wait goes on (it carries on once approved).
    @Test func approvalIsSaidAndTheWaitGoesOn() async {
        let reports = Reports()
        var pings = 0
        let approval = BackgroundServiceProblem(message: ServiceText.needsApproval, opensLoginItems: true, offersTryAgain: false)
        let outcome = await LaunchWait.untilAnswered(
            ping: { pings += 1; return pings > 3 * LaunchWait.pingsPerCheck },
            problem: { approval },
            report: { reports.all.append($0) },
            sleep: { _ in })
        #expect(outcome == .answered)
        #expect(reports.all.first == .problem(approval))
        #expect(!reports.all.contains(.notAnswering)) // the approval says why
    }

    /// launchd refusing the login item ends the wait at once, with its
    /// problem.
    @Test func aRefusalEndsTheWait() async {
        let refused = BackgroundServiceProblem(message: ServiceText.notStarted, opensLoginItems: true, offersTryAgain: true)
        let outcome = await LaunchWait.untilAnswered(
            ping: { false }, problem: { refused }, report: { _ in }, sleep: { _ in })
        #expect(outcome == .refused(refused))
    }

    /// Cancelled (Try Again starts a new wait): the wait ends without an
    /// answer.
    @Test func cancellingEndsTheWait() async {
        let task = Task { @MainActor in
            await LaunchWait.untilAnswered(ping: { false }, problem: { nil }, report: { _ in },
                                           sleep: { _ in await Task.yield() })
        }
        task.cancel()
        #expect(await task.value == .cancelled)
    }

    /// What shows while portenvd isn't answering: Welcome on a Mac with no
    /// earlier run; with one recorded, the empty list with the notice.
    @Test func whatShowsWhileTheServiceIsntAnswering() {
        #expect(LaunchWait.fallback(lastBox: nil, firstRunDone: false) == .firstRun(.welcome))
        #expect(LaunchWait.fallback(lastBox: "acme-api", firstRunDone: false) == .boxList(notice: FirstRunText.serviceNotAnswering))
        #expect(LaunchWait.fallback(lastBox: nil, firstRunDone: true) == .boxList(notice: FirstRunText.serviceNotAnswering))
    }

    /// Welcome reached that way already says the service isn't answering,
    /// and clears it once portenvd answers Docker's check.
    @Test func welcomeStartsWithTheNotice() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil, serviceGone: true)
        #expect(flow.daemonGone)
        #expect(flow.continueHint == FirstRunText.serviceNotAnswering)
        await flow.checkDocker { nil }
        #expect(flow.daemonGone)
        await flow.checkDocker { .running }
        #expect(!flow.daemonGone)
    }

    /// The merged launch order (R-0121, R-0124): the login item is
    /// registered, the move sheet is only shown, and the launch wait runs
    /// to its notice and its answer while the question stays unanswered.
    @Test func theMoveQuestionNeverHoldsUpTheWait() async {
        var steps: [String] = []
        var offered = false
        var pings = 0
        await LaunchOrder.begin(
            offersMove: true,
            register: { steps.append("register") },
            offer: { offered = true; steps.append("offer") },
            wait: {
                let outcome = await LaunchWait.untilAnswered(
                    ping: { pings += 1; return pings > 2 * LaunchWait.pingsPerCheck },
                    problem: { nil },
                    report: { steps.append("\($0)") },
                    sleep: { _ in })
                steps.append("\(outcome)")
            })
        #expect(offered) // shown, never answered
        #expect(steps == ["register", "offer", "notAnswering", "answered"])
    }

    /// An answer ends the wait: no more asking, no more registering.
    @Test func theWaitStopsAfterAnAnswer() async {
        var pings = 0
        let outcome = await LaunchWait.untilAnswered(
            ping: { pings += 1; return pings == 3 }, problem: { nil }, report: { _ in }, sleep: { _ in })
        #expect(outcome == .answered)
        #expect(pings == 3)
    }

    /// The login item's status is read right after the first unanswered
    /// ask, not 2 s later (R-0125): approval is said at once, a refusal
    /// ends the wait at once.
    @Test func theStatusIsReadBeforeTheFirstRound() async {
        var pings = 0
        let refused = BackgroundServiceProblem(message: ServiceText.notStarted, opensLoginItems: true, offersTryAgain: true)
        let outcome = await LaunchWait.untilAnswered(
            ping: { pings += 1; return false }, problem: { refused }, report: { _ in }, sleep: { _ in })
        #expect(outcome == .refused(refused))
        #expect(pings == 1)

        let approval = BackgroundServiceProblem(message: ServiceText.needsApproval, opensLoginItems: true, offersTryAgain: false)
        var reports: [LaunchWait.Report] = []
        var pingsAtReport = -1
        pings = 0
        _ = await LaunchWait.untilAnswered(
            ping: { pings += 1; return pings > 5 }, problem: { approval },
            report: { reports.append($0); if pingsAtReport < 0 { pingsAtReport = pings } }, sleep: { _ in })
        #expect(reports == [.problem(approval)])
        #expect(pingsAtReport == 1)
    }

    /// Try Again cancels a wait whose answer is in flight: the old wait
    /// never decides, so only one launch applies (R-0125).
    @Test func aCancelledWaitNeverDecides() async {
        var decided = 0
        let (go, release) = AsyncStream<Void>.makeStream()
        let task = Task { @MainActor in
            await LaunchWait.run(ping: { for await _ in go { break }; return true },
                                 problem: { nil }, report: { _ in }, sleep: { _ in },
                                 boxes: { ["acme-api"] }, decide: { _ in decided += 1 })
        }
        await Task.yield()
        task.cancel()
        release.yield()
        #expect(await task.value == .cancelled)
        #expect(decided == 0)
    }

    /// Cancelled while the list of workspaces is read: no decision either.
    @Test func cancelledDuringTheListNeverDecides() async {
        var decided = 0
        let (go, release) = AsyncStream<Void>.makeStream()
        let task = Task { @MainActor in
            await LaunchWait.run(ping: { true }, problem: { nil }, report: { _ in }, sleep: { _ in },
                                 boxes: { for await _ in go { break }; return ["acme-api"] },
                                 decide: { _ in decided += 1 })
        }
        await Task.yield()
        task.cancel()
        release.yield()
        #expect(await task.value == .cancelled)
        #expect(decided == 0)
    }

    /// Answered: the list is read and the decision made once.
    @Test func anAnsweredWaitDecidesOnce() async {
        var decided: [[String]] = []
        let outcome = await LaunchWait.run(ping: { true }, problem: { nil }, report: { _ in }, sleep: { _ in },
                                           boxes: { ["acme-api"] }, decide: { decided.append($0) })
        #expect(outcome == .decided)
        #expect(decided == [["acme-api"]])
    }

    /// The list failing is said, not decided.
    @Test func aFailedListIsSaid() async {
        struct Failed: Error, LocalizedError { var errorDescription: String? { "no list" } }
        let outcome = await LaunchWait.run(ping: { true }, problem: { nil }, report: { _ in }, sleep: { _ in },
                                           boxes: { throw Failed() }, decide: { _ in Issue.record("decided") })
        #expect(outcome == .failed("no list"))
    }

    /// An answer never pulls the person off a screen they moved to from
    /// the fallback (R-0125): only the list of workspaces is refreshed.
    @Test func anAnswerNeverPullsThePersonAway() {
        let list = LaunchWait.fallback(lastBox: "acme-api", firstRunDone: true)
        // Still on the fallback list: the decision applies.
        #expect(LaunchWait.applies(.open("acme-api"), fallback: list, current: list, firstRunStep: nil))
        // Moved on to New Workspace…: refresh only.
        #expect(!LaunchWait.applies(.open("acme-api"), fallback: list, current: .firstRun(.firstBox), firstRunStep: .firstBox))
        // Back on the list without the notice: refresh only.
        #expect(!LaunchWait.applies(.open("acme-api"), fallback: list, current: .boxList(notice: nil), firstRunStep: nil))

        let welcome = LaunchWait.fallback(lastBox: nil, firstRunDone: false)
        // Still on Welcome itself: a decision elsewhere applies; Welcome again keeps it.
        #expect(LaunchWait.applies(.open("made-with-cli"), fallback: welcome, current: welcome, firstRunStep: .welcome))
        #expect(!LaunchWait.applies(.firstRun(.welcome), fallback: welcome, current: welcome, firstRunStep: .welcome))
        // Moved on from Welcome to a later step: refresh only.
        #expect(!LaunchWait.applies(.open("made-with-cli"), fallback: welcome, current: welcome, firstRunStep: .protection))
        // No fallback shown: the decision applies.
        #expect(LaunchWait.applies(.open("acme-api"), fallback: nil, current: nil, firstRunStep: nil))
    }

    /// A wait replaced while the status says launchd refused never sets
    /// the problem: it ends as cancelled (R-0132).
    @Test func aReplacedWaitNeverReportsARefusal() async {
        let refused = BackgroundServiceProblem(message: ServiceText.notStarted, opensLoginItems: true, offersTryAgain: true)
        let (go, release) = AsyncStream<Void>.makeStream()
        let task = Task { @MainActor in
            await LaunchWait.run(ping: { for await _ in go { break }; return false },
                                 problem: { refused }, report: { _ in }, sleep: { _ in },
                                 boxes: { [] }, decide: { _ in Issue.record("decided") })
        }
        await Task.yield()
        task.cancel()
        release.yield()
        #expect(await task.value == .cancelled)
    }

    /// Only the last window closing stops the wait: closing a second
    /// window (File › New Window) leaves the first one's launch going
    /// (R-0132).
    @Test func onlyTheLastWindowStopsTheWait() {
        var windows = LaunchWindows()
        windows.appeared()
        windows.appeared()
        let first = windows.disappeared()
        #expect(!first) // one is left
        let last = windows.disappeared()
        #expect(last) // the last one
        let again = windows.disappeared()
        #expect(!again) // never below zero
    }

    /// The spinner has words for VoiceOver.
    @Test func theSpinnerIsLabelled() {
        #expect(LaunchWait.starting == "Starting Portenv\u{2026}")
    }
}
