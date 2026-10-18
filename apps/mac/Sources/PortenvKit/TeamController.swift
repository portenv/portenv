// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the window shows: every tab, the owner's own, or one agent's tabs
/// and column (R-0240, R-0241).
public enum TeamSelection: Equatable, Hashable, Sendable {
    case allTabs
    case you
    case agent(String)
    /// A decision Portenv itself asks (the team's first start), by id.
    case decision(String)
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
    /// The agent file on show for Approve, fetched from portenvd.
    public private(set) var review: AgentFileReview?
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
    /// decision can be answered. The team's first start is portenvd's to
    /// carry out: its Approve starts the agents the decision names, there,
    /// so it works whatever this app remembers (R-0296). An answered
    /// decision shown on its own closes, to the first agent it starts.
    public func answer(_ decision: TeamDecision, approve: Bool, note: String = "") async {
        guard decision.answerable else { return }
        await perform {
            let d = try await self.daemon.answerDecision(self.box, id: decision.id, approve: approve, note: note)
            self.state.apply(.decision(d))
            if self.selection == .decision(d.id), !d.answerable {
                let next = d.state == .approved ? d.startAgents.first { self.state.agent($0) != nil } : nil
                self.selection = next.map { .agent($0) } ?? .allTabs
            }
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

    /// Whether Start is on: a stopped agent of this workspace whose file
    /// is approved (R-0285). Portenv never starts remote agents.
    public func canStart(_ a: TeamAgent) -> Bool { a.approved && !a.remote && a.status == .stopped }

    /// Starts an agent whose file is approved. The team's first start waits
    /// for the owner's team-start decision, which names this agent;
    /// approving it starts the agent in portenvd.
    public func start(_ agent: String) async {
        if let a = state.agent(agent), !a.approved {
            error = TeamText.approveFirst(a.name)
            return
        }
        await perform {
            self.state.apply(.agent(try await self.daemon.startAgent(self.box, id: agent)))
        }
    }

    public func stop(_ agent: String) async {
        await perform { self.state.apply(.agent(try await self.daemon.stopAgent(self.box, id: agent))) }
    }

    /// The decisions shown in an agent's column: what it asks, and the
    /// team's first start while it names this agent.
    public func decisions(for agent: String) -> [TeamDecision] {
        state.needsYou.filter { $0.from == agent || ($0.kind == .teamStart && $0.startAgents.contains(agent)) }
    }

    /// The decision selected on its own (one Portenv asks).
    public var selectedDecision: TeamDecision? {
        if case .decision(let id) = selection { return state.decisions.first { $0.id == id } }
        return nil
    }

    /// Fetches an agent's file from portenvd for Approve: the whole text,
    /// with its hash worked out here. Nothing is shown to approve when the
    /// text doesn't hash to what portenvd says.
    public func showFile(_ agent: String) async {
        review = nil
        await perform {
            let file = try await self.daemon.getAgentFile(self.box, id: agent)
            guard let r = AgentFileReview(agentID: agent, file: file) else { throw DaemonError(TeamText.fileNotShownExactly) }
            self.review = r
        }
    }

    /// The owner's Approve on the file the app showed: pins that text's
    /// hash. Only a review this controller fetched from portenvd and is
    /// showing can be approved; nothing an agent sends gets here.
    public func approve(_ shown: AgentFileReview) async {
        guard shown == review else { return }
        await perform {
            let a = try await self.daemon.approveAgentFile(self.box, id: shown.agentID, sha256: shown.sha256)
            self.state.apply(.agent(a))
            self.review = nil
        }
    }

    public func closeFile() { review = nil }

    /// Tests: an event as if from the stream.
    func receive(_ event: TeamEvent) { state.apply(event) }

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
