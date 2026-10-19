// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The workspaces running on this Mac (ADR 0023): open here, with or without
/// a window. The list shows them as "Running", and Quit closes them.
public enum RunningWorkspaces {
    /// Each workspace portenvd knows whose state says it's open on this Mac:
    /// not closed, not on a server. One that doesn't answer isn't counted.
    public static func here(_ daemon: DaemonAPI) async -> [String] {
        guard let boxes = try? await daemon.boxes() else { return [] }
        var running: [String] = []
        for box in boxes {
            guard let st = try? await daemon.state(box), st.save != .closed, st.location == nil else { continue }
            running.append(box)
        }
        return running
    }
}

/// Quitting Portenv closes every workspace running on this Mac (ADR 0023,
/// R-0238): it names them first, then closes (saves and releases) each in
/// turn, asking on a failed save as quitting always has (QuitFlow).
public struct QuitAll: Sendable {
    let daemon: DaemonAPI
    public let boxes: [String]
    /// portenvd didn't answer (R-0252): nothing can be saved or closed, so
    /// Quit asks first and closes nothing; the workspaces keep running.
    public let unreachable: Bool

    public init(daemon: DaemonAPI, boxes: [String], unreachable: Bool = false) {
        self.daemon = daemon
        self.boxes = boxes
        self.unreachable = unreachable
    }

    /// The running workspaces, read now. When portenvd doesn't answer, the
    /// ones the app already knew were running (`known`) are named instead.
    public static func prepare(_ daemon: DaemonAPI, known: [String] = []) async -> QuitAll {
        guard (try? await daemon.boxes()) != nil else {
            return QuitAll(daemon: daemon, boxes: known.sorted(), unreachable: true)
        }
        return QuitAll(daemon: daemon, boxes: await RunningWorkspaces.here(daemon))
    }

    /// The confirmation's text, or nil when nothing is running. Always one
    /// when portenvd doesn't answer: quitting then never stops or loses a
    /// workspace silently.
    public var confirmation: String? {
        if unreachable {
            let head = "Portenv's background service isn't running, so"
            switch boxes.count {
            case 0: return "\(head) no workspace can be saved now. Any that are running keep running; quit anyway?"
            case 1: return "\(head) \(boxes[0]) can't be saved now. It keeps running; quit anyway?"
            default: return "\(head) \(boxes.dropLast().joined(separator: ", ")) and \(boxes.last!) can't be saved now. They keep running; quit anyway?"
            }
        }
        switch boxes.count {
        case 0: return nil
        case 1: return "\(boxes[0]) is running. Quitting saves and closes it."
        default: return "\(boxes.dropLast().joined(separator: ", ")) and \(boxes.last!) are running. Quitting saves and closes them."
        }
    }

    /// Closes each, in order. Returns true to quit, false when the person
    /// chose to stay (the rest keep running).
    public func run(ask: @Sendable (String) async -> QuitChoice, tell: @Sendable (String) async -> Void = { _ in }) async -> Bool {
        // Nothing can be closed: each keeps running, for the next portenvd
        // to take over with its tabs (the person already said quit anyway).
        if unreachable { return true }
        for box in boxes {
            guard await QuitFlow(daemon: daemon, box: box, openHere: true).run(ask: ask, tell: tell) else { return false }
        }
        return true
    }
}

/// The list's words for a running workspace (ADR 0023).
public enum BoxListText {
    public static let running = "Running"
    public static let closeWorkspace = "Close Workspace"
    public static func label(_ box: String, running: Bool) -> String {
        running ? "Workspace \(box), running" : "Workspace \(box)"
    }
}
