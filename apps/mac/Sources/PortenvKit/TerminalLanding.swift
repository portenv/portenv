// SPDX-License-Identifier: Apache-2.0

/// Where a tab's terminal lands when the owner switches to it (the owner,
/// 2026-10-22; GUIDELINES §4.1): choosing another agent, You or All tabs in
/// the sidebar, or another tab in the bar.
public enum TerminalLanding: Equatable, Sendable {
    /// At the bottom: the live prompt and the cursor's line.
    case bottom
    /// Where it was.
    case keep

    /// A switch lands at the prompt, unless the owner had scrolled up in
    /// that tab themselves and nothing new has arrived there since: then
    /// their place holds. Without a switch nothing moves, so output while
    /// they read back never pulls the view down.
    public static func on(switched: Bool, scrolledUp: Bool, newOutputSince: Bool) -> TerminalLanding {
        guard switched, scrolledUp, !newOutputSince else { return switched ? .bottom : .keep }
        return .keep
    }
}
