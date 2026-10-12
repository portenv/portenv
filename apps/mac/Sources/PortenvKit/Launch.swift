// SPDX-License-Identifier: Apache-2.0

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
        "There’s no box called “\(box)” on this Mac. Choose one of your boxes, or make a new one."
    }
}

/// The launch's wait for portenvd (R-0121): never a spinner without end.
/// portenvd is asked every 200 ms; each 2 s without an answer is a missed
/// check. The login item's status is read at each missed check: waiting for
/// approval is said as such and the wait goes on; launchd refusing the item
/// ends the wait. Otherwise two missed checks in a row (about 4 s, as on
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
                                     sleep: (Duration) async -> Void) async -> Outcome {
        var misses = 0
        var last: Report?
        while true {
            for _ in 0 ..< pingsPerCheck {
                if Task.isCancelled { return .cancelled }
                if await ping() { return .answered }
                await sleep(pollEvery)
            }
            misses += 1
            let now: Report?
            if let p = problem() {
                if p.endsWait { return .refused(p) }
                now = .problem(p)
            } else {
                now = misses >= missesBeforeNotice ? .notAnswering : nil
            }
            if let now, now != last {
                report(now)
                last = now
            }
        }
    }

    /// What shows while portenvd isn't answering: Welcome on a Mac with no
    /// earlier run; with one recorded, the (empty) list with the notice.
    public static func fallback(lastBox: String?, firstRunDone: Bool) -> Launch {
        lastBox != nil || firstRunDone ? .boxList(notice: FirstRunText.serviceNotAnswering) : .firstRun(.welcome)
    }
}
