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

    public init(daemon: DaemonAPI, boxes: [String]) {
        self.daemon = daemon
        self.boxes = boxes
    }

    /// The running workspaces, read now.
    public static func prepare(_ daemon: DaemonAPI) async -> QuitAll {
        QuitAll(daemon: daemon, boxes: await RunningWorkspaces.here(daemon))
    }

    /// The confirmation's text, or nil when nothing is running.
    public var confirmation: String? {
        switch boxes.count {
        case 0: nil
        case 1: "\(boxes[0]) is running. Quitting saves and closes it."
        default: "\(boxes.dropLast().joined(separator: ", ")) and \(boxes.last!) are running. Quitting saves and closes them."
        }
    }

    /// Closes each, in order. Returns true to quit, false when the person
    /// chose to stay (the rest keep running).
    public func run(ask: @Sendable (String) async -> QuitChoice) async -> Bool {
        for box in boxes {
            guard await QuitFlow(daemon: daemon, box: box, openHere: true).run(ask: ask) else { return false }
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
