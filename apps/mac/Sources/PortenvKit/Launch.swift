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
