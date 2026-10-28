// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the app shows when it starts (PLAN.md 1.5). Never a window for a box
/// that doesn't exist: with no box, first run; with a box that isn't here,
/// the box list, saying so.
public enum Launch: Equatable, Sendable {
    /// First run, from this step: the whole of it, or only the first box
    /// when first run was finished before and every box has since gone.
    case firstRun(FirstRunStep)
    /// A box that exists on this Mac.
    case open(String)
    /// The box list, with a line saying why it's shown, if any.
    case boxList(notice: String?)
}

public enum LaunchDecision {
    /// - requested: the box asked for (PORTENV_BOX, or a box opened from
    ///   Finder or the CLI), if any.
    /// - boxes: the boxes on this Mac (portenvd's ListBoxes).
    /// - lastBox: the box open when the app last quit.
    /// - firstRunDone: whether first run was finished on this Mac.
    public static func decide(requested: String?, boxes: [String], lastBox: String?, firstRunDone: Bool) -> Launch {
        if boxes.isEmpty {
            return .firstRun(firstRunDone ? .firstBox : .welcome)
        }
        if let requested, !requested.isEmpty {
            if boxes.contains(requested) { return .open(requested) }
            return .boxList(notice: missing(requested))
        }
        if let lastBox {
            return boxes.contains(lastBox) ? .open(lastBox) : .boxList(notice: nil)
        }
        return boxes.count == 1 ? .open(boxes[0]) : .boxList(notice: nil)
    }

    /// GUIDELINES §10: what happened, then what to do next.
    public static func missing(_ box: String) -> String {
        "There’s no workspace called “\(box)” on this Mac. Choose one of your workspaces, or make a new one."
    }
}

/// The launch's wait for portenvd (R-0121): never a spinner without end.
/// portenvd is asked every 200 ms; each 2 s without an answer is a missed
/// check. The login item's status is read at each missed check: waiting for
/// approval is said as such and the wait goes on; launchd refusing the item
/// ends the wait, after one silent re-register (R-0401). Otherwise two missed checks in a row (about 4 s, as on
/// Welcome) say the service isn't answering, and the checks go on.
public enum LaunchWait {
    /// What the window says while there's no answer.
    public enum Report: Equatable, Sendable {
        case problem(BackgroundServiceProblem)
        case notAnswering
    }

    public enum Outcome: Equatable, Sendable {
        case answered
        case refused(BackgroundServiceProblem)
        case cancelled
    }

    public static let pollEvery: Duration = .milliseconds(200)
    /// One check: 2 s of asking.
    public static let pingsPerCheck = 10
    public static let missesBeforeNotice = 2
    /// The spinner's words for VoiceOver.
    public static let starting = "Starting Portenv\u{2026}"

    /// Waits until ping answers, reporting each change in what the window
    /// should say.
    @MainActor
    public static func untilAnswered(ping: () async -> Bool,
                                     problem: () -> BackgroundServiceProblem?,
                                     report: (Report) -> Void,
                                     sleep: (Duration) async -> Void,
                                     reRegister: () async -> Void = {}) async -> Outcome {
        var misses = 0
        var last: Report?
        // One silent re-register per wait (R-0401): a rebuilt app's
        // portenvd that launchd won't spawn, or a login item that didn't
        // register, is registered afresh before anything is said. Try
        // Again starts a new wait, so it never loops by itself.
        var reRegistered = false
        // Round 0 is the first ask alone: the login item's status is read
        // at once after it (R-0125), so approval is said and a refusal acted
        // on without waiting 2 s. Each later round is one check (2 s).
        var round = 0
        while true {
            if round == 0 {
                if Task.isCancelled { return .cancelled }
                if await ping() { return .answered }
            } else {
                // The first check already made its first ask.
                for _ in 0 ..< (round == 1 ? pingsPerCheck - 1 : pingsPerCheck) {
                    if Task.isCancelled { return .cancelled }
                    if await ping() { return .answered }
                    await sleep(pollEvery)
                }
                misses += 1
            }
            // A wait replaced while it asked says nothing (R-0133).
            if Task.isCancelled { return .cancelled }
            let p = problem()
            if let p, p.reRegisterFirst, !reRegistered {
                reRegistered = true
                await reRegister()
                if Task.isCancelled { return .cancelled }
                // A whole check for the fresh registration to come up
                // before its status counts.
                misses = 0
                round = 1
                continue
            }
            if let p, p.endsWait { return .refused(p) }
            let now: Report? = p.map { .problem($0) } ?? (misses >= missesBeforeNotice ? .notAnswering : nil)
            if let now, now != last {
                report(now)
                last = now
            }
            if round == 0 { await sleep(pollEvery) }
            round += 1
        }
    }

    public enum RunOutcome: Equatable, Sendable {
        case decided
        case refused(BackgroundServiceProblem)
        case failed(String)
        case cancelled
    }

    /// The whole launch wait: untilAnswered, then the list of workspaces,
    /// then decide. A cancelled wait (Try Again started another, or the
    /// window closed) never decides, even if its answer was in flight
    /// (R-0125).
    @MainActor
    public static func run(ping: () async -> Bool,
                           problem: () -> BackgroundServiceProblem?,
                           report: (Report) -> Void,
                           sleep: (Duration) async -> Void,
                           reRegister: () async -> Void = {},
                           boxes: () async throws -> [String],
                           decide: ([String]) -> Void) async -> RunOutcome {
        switch await untilAnswered(ping: ping, problem: problem, report: report, sleep: sleep, reRegister: reRegister) {
        case .answered: break
        // A replaced wait never sets the problem (R-0132).
        case let .refused(p): return Task.isCancelled ? .cancelled : .refused(p)
        case .cancelled: return .cancelled
        }
        guard !Task.isCancelled else { return .cancelled }
        let listed: [String]
        do {
            listed = try await boxes()
        } catch {
            guard !Task.isCancelled else { return .cancelled }
            return .failed((error as? LocalizedError)?.errorDescription ?? error.localizedDescription)
        }
        guard !Task.isCancelled else { return .cancelled }
        decide(listed)
        return .decided
    }

    /// Whether portenvd's answer replaces what shows (R-0125). Only while
    /// the fallback still shows and the person hasn't moved on from it
    /// (Welcome past its first step, New Workspace…, the list without the
    /// notice); otherwise the answer only refreshes the list of workspaces.
    /// Welcome deciding Welcome keeps the one showing.
    public static func applies(_ decision: Launch, fallback: Launch?, current: Launch?, firstRunStep: FirstRunStep?) -> Bool {
        guard let fallback else { return true }
        guard current == fallback else { return false }
        if case .firstRun = fallback, firstRunStep != .welcome { return false }
        return decision != current
    }

    /// What shows while portenvd isn't answering: Welcome on a Mac with no
    /// earlier run; with one recorded, the (empty) list with the notice.
    public static func fallback(lastBox: String?, firstRunDone: Bool) -> Launch {
        lastBox != nil || firstRunDone ? .boxList(notice: FirstRunText.serviceNotAnswering) : .firstRun(.welcome)
    }
}

/// The app's windows showing the launch (R-0132): the launch wait stops
/// only when the last one closes, never when a second window does.
public struct LaunchWindows: Sendable {
    public private(set) var open = 0
    public init() {}
    public mutating func appeared() { open += 1 }
    /// A window closed; true when it was the last.
    public mutating func disappeared() -> Bool {
        guard open > 0 else { return false }
        open -= 1
        return open == 0
    }
}

/// What the window says while the launch catches up with itself (basics
/// audit row 28, R-0386), never a bare background: a workspace opening
/// before its controller exists, or first run before its flow does.
public enum LaunchGap {
    /// The line to show, or nil when the screen has what it needs.
    public static func line(_ launch: Launch?, hasController: Bool, hasFirstRunFlow: Bool) -> String? {
        switch launch {
        case let .open(box) where !hasController: TabTerminalShown.openingLine(tab: box)
        case .firstRun where !hasFirstRunFlow: LaunchWait.starting
        default: nil
        }
    }

    /// Where the window goes when the box went before its open began: the
    /// list, saying so, as at launch.
    public static func boxGone(_ box: String) -> Launch {
        .boxList(notice: LaunchDecision.missing(box))
    }
}
