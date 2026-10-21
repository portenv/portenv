// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The teams of workspaces kept running with no window (ADR 0023): the app
/// follows each one itself, with the same WatchTeam a window uses, so a
/// decision there still notifies and counts in the Dock badge (§5, R-0388).
/// A workspace a window shows is that window's to follow; the hand-over
/// either way keeps the count (NeedsYouCenter is keyed by workspace).
@MainActor
public final class WindowlessTeams {
    private let daemon: DaemonAPI
    private let center: NeedsYouCenter
    private let retry: Duration
    /// The workspaces a window shows now (the app sets it).
    public var shown: @MainActor () -> Set<String> = { [] }
    private var teams: [String: TeamController] = [:]
    private var tasks: [String: Task<Void, Never>] = [:]

    public init(daemon: DaemonAPI, center: NeedsYouCenter, retry: Duration = .seconds(1)) {
        self.daemon = daemon
        self.center = center
        self.retry = retry
    }

    /// The workspaces followed here, sorted.
    public var following: [String] { teams.keys.sorted() }

    /// The workspaces running on this Mac, as portenvd last said: follows
    /// each one no window shows, and stops following one that stopped
    /// running (out of the badge, its notifications taken back).
    public func update(running: Set<String>) {
        let want = running.subtracting(shown())
        for box in teams.keys where !want.contains(box) {
            stop(box)
            if !running.contains(box) { center.closed(box: box) }
        }
        for box in want where teams[box] == nil { follow(box) }
    }

    /// A window now shows the workspace: it follows the team from here on.
    /// Nothing is taken back; the window's team reports to the same count.
    public func windowOpened(_ box: String) { stop(box) }

    /// The window no longer shows it; still running, it's followed here.
    public func windowClosed(_ box: String, running: Bool) {
        if running, teams[box] == nil, !shown().contains(box) { follow(box) }
    }

    /// What Show selects for a decision in a workspace followed here.
    public func selection(for route: NotificationRoute) -> TeamSelection? {
        guard case .decision(let box, let id) = route, let team = teams[box] else { return nil }
        return NotificationRoute.selection(decision: id, in: team.state)
    }

    /// Stops following every workspace (quitting).
    public func stopAll() {
        for box in teams.keys { stop(box) }
    }

    private func follow(_ box: String) {
        let team = TeamController(box: box, daemon: daemon, retry: retry)
        // Running here, so on this Mac (RunningWorkspaces.here).
        team.changed = { [center] state in center.team(box: box, state: state, location: nil) }
        teams[box] = team
        tasks[box] = Task { await team.follow() }
    }

    private func stop(_ box: String) {
        tasks.removeValue(forKey: box)?.cancel()
        teams[box]?.changed = nil
        teams[box] = nil
    }
}
