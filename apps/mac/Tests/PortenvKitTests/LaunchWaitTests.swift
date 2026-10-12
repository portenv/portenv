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

    /// The spinner has words for VoiceOver.
    @Test func theSpinnerIsLabelled() {
        #expect(LaunchWait.starting == "Starting Portenv\u{2026}")
    }
}
