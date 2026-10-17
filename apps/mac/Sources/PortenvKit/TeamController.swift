// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the window shows: every tab, the owner's own, or one agent's tabs
/// and column (R-0240, R-0241).
public enum TeamSelection: Equatable, Hashable, Sendable {
    case allTabs
    case you
    case agent(String)
}

/// A workspace's team, followed from portenvd, and the owner's actions on
/// it. Every action goes through portenvd; decisions are answered only here
/// (ADR 0022 §6).
@MainActor
@Observable
public final class TeamController {
    public let box: String
    public private(set) var state = TeamState()
    public var selection: TeamSelection = .allTabs
    /// The last failure, one plain line; cleared by dismissError.
    public private(set) var error: String?
    /// Whether this portenvd knows about teams at all (an older one says
    /// it's out of date): the sidebar stays hidden when it doesn't.
    public private(set) var supported = true
    private let daemon: DaemonAPI
    private let retry: Duration

    public init(box: String, daemon: DaemonAPI, retry: Duration = .seconds(1)) {
        self.box = box
        self.daemon = daemon
        self.retry = retry
    }

    /// The selected agent, if one is selected and still in the team.
    public var selectedAgent: TeamAgent? {
        if case .agent(let id) = selection { return state.agent(id) }
        return nil
    }

    /// Follows the team from where it got to, watching again after the
    /// stream ends (the box restarted, or portenvd went away). Runs until
    /// the task is cancelled, or portenvd turns out not to know teams.
    public func follow() async {
        while !Task.isCancelled {
            do {
                for try await (event, cursor) in daemon.watchTeam(box, cursor: state.cursor) {
                    state.apply(event, cursor: cursor)
                    if case .removed(let id) = event, selection == .agent(id) { selection = .allTabs }
                }
            } catch let e as DaemonError where e.message == DaemonError.outOfDate {
                supported = false
                return
            } catch {}
            if Task.isCancelled { return }
            try? await Task.sleep(for: retry)
        }
    }

    /// Approve or Decline (with the owner's reason); only a pending
    /// decision can be answered.
    public func answer(_ decision: TeamDecision, approve: Bool, note: String = "") async {
        guard decision.answerable else { return }
        await perform {
            let d = try await self.daemon.answerDecision(self.box, id: decision.id, approve: approve, note: note)
            self.state.apply(.decision(d))
        }
    }

    /// The owner's message to an agent. Nothing is sent when it's empty.
    public func send(_ text: String, to agent: String, thread: String = "") async {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        await perform {
            let m = try await self.daemon.sendMessage(self.box, to: agent, thread: thread, text: text)
            self.state.apply(.message(m))
        }
    }

    public func start(_ agent: String) async {
        await perform { self.state.apply(.agent(try await self.daemon.startAgent(self.box, id: agent))) }
    }

    public func stop(_ agent: String) async {
        await perform { self.state.apply(.agent(try await self.daemon.stopAgent(self.box, id: agent))) }
    }

    public func dismissError() { error = nil }

    private func perform(_ work: @MainActor () async throws -> Void) async {
        do {
            try await work()
            error = nil
        } catch let e as DaemonError {
            error = e.message
        } catch {
            self.error = error.localizedDescription
        }
    }
}
