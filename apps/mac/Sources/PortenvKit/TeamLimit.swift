// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The team against the owner's agent limit (R-0347), from portenvd: the
/// Team section's line and, when the team needs more, the Raise it offers.
/// portenvd enforces the limit; the app only shows it and asks to change it.
public struct TeamLimit: Equatable, Sendable {
    /// Settings › Team › Agents allowed.
    public var allowed: Int
    /// The agent files in the team, and the agents starting or running.
    public var inTeam: Int
    public var running: Int
    /// portenvd's plain line: empty when the team is well under the limit.
    public var line: String
    /// The number the owner may raise the limit to (at most 8); 0 when the
    /// team doesn't need more.
    public var raiseTo: Int

    public init(allowed: Int, inTeam: Int, running: Int, line: String, raiseTo: Int) {
        self.allowed = allowed
        self.inTeam = inTeam
        self.running = running
        self.line = TeamText.line(line, max: 300)
        self.raiseTo = raiseTo
    }

    /// Whether the Team section shows the line.
    public var shows: Bool { !line.isEmpty }
    /// Over the limit (the team, or the agents running after a lower
    /// setting): shown as a warning. At or near it: a quiet line.
    public var isWarning: Bool { inTeam > allowed || running > allowed }

    /// The one-click raise; nil when there's nothing to raise to.
    public var raiseTitle: String? { raiseTo > allowed ? "Raise to \(raiseTo)\u{2026}" : nil }
    /// The owner's confirmation before the raise.
    public var confirmTitle: String { "Allow \(raiseTo) agents in each workspace?" }
    public var confirmMessage: String {
        "Agents allowed in Settings \u{203A} Team goes from \(allowed) to \(raiseTo), for every workspace on this Mac. You can lower it there."
    }
    public var confirmButton: String { "Allow \(raiseTo)" }
}

/// Settings › Team › Agents allowed (R-0347): how many agents may run at
/// once in one workspace. The owner's setting, kept by portenvd.
public enum AgentsAllowed {
    public static let range = 1 ... 8
    public static let defaultValue = 4
    public static var choices: [Int] { Array(range) }

    /// Sets it through portenvd, which refuses anything outside 1 to 8.
    /// Settings, the Team section's Raise and, later, the New-workspace
    /// Team step (ADR 0025) all call this, after the owner chose it.
    @discardableResult
    public static func set(_ n: Int, daemon: DaemonAPI) async throws -> Int {
        try await daemon.setAgentsAllowed(n)
    }
}
