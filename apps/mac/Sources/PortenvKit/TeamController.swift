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
    /// Team activity: the whole team's messages (R-0313).
    case activity
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
    /// Told after the team changes (from the stream, or an answer given
    /// here): the session's notifications and the Dock badge (§5).
    @ObservationIgnored public var changed: (@MainActor (TeamState) -> Void)?
    /// A decision to select once the team has it (a notification's Show
    /// opened this window before its team arrived).
    @ObservationIgnored private var toShow: String?

    public init(box: String, daemon: DaemonAPI, retry: Duration = .seconds(1), github: GitHubAccount = .system) {
        self.box = box
        self.daemon = daemon
        self.retry = retry
        self.github = github
        push = PushController(box: box, daemon: daemon, github: github)
    }

    /// The workspace's pushes waiting for the app (R-0391).
    public let push: PushController

    private let github: GitHubAccount

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
            if !teamPlanChecked { await applyKeptTeam() }
            do {
                for try await (event, cursor) in daemon.watchTeam(box, cursor: state.cursor) {
                    state.apply(event, cursor: cursor)
                    if case .removed(let id) = event, selection == .agent(id) { selection = .allTabs }
                    didChange()
                }
            } catch let e as DaemonError where e.message == DaemonError.outOfDate {
                supported = false
                return
            } catch {}
            if Task.isCancelled { return }
            try? await Task.sleep(for: retry)
        }
    }

    /// Selects a decision as its Needs-you row does, now or as soon as
    /// the team has it; once only, so the owner's own choice afterwards
    /// stands.
    public func show(decision id: String) {
        toShow = id
        showPending()
    }

    private func showPending() {
        guard let id = toShow, let s = NotificationRoute.selection(decision: id, in: state) else { return }
        selection = s
        toShow = nil
    }

    /// The team changed: a decision waiting to be shown, then whoever
    /// listens.
    private func didChange() {
        showPending()
        changed?(state)
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
            self.answered(d)
        }
    }

    /// Answers a decision with choices with the one at `index` (counting
    /// from 0, as the card lists them): portenvd gets its number and its
    /// label, and refuses one that isn't the decision's (CC-0501).
    public func choose(_ decision: TeamDecision, index: Int) async {
        guard decision.answerable, decision.choices.indices.contains(index) else { return }
        await perform {
            let d = try await self.daemon.chooseDecision(self.box, id: decision.id, choice: index + 1, label: decision.choices[index])
            self.answered(d)
        }
    }

    private func answered(_ d: TeamDecision) {
        state.apply(.decision(d))
        didChange()
        if selection == .decision(d.id), !d.answerable {
            let next = d.state == .approved ? d.startAgents.first { state.agent($0) != nil } : nil
            selection = next.map { .agent($0) } ?? .allTabs
        }
    }

    /// Lines from writing the team New workspace kept (ADR 0025): a file
    /// the project already had, or one that isn't what was approved.
    public private(set) var setupLines: [String] = []
    private var teamPlanChecked = false
    /// The project couldn't be brought in (a failed clone): the window's
    /// line, with Try Again.
    public private(set) var setupProblem: SetupProblem?
    /// ApplyTeamPlan is working (a clone can take a while).
    public private(set) var settingUp = false

    /// Brings in the project and writes and pins the team New workspace
    /// kept, once the workspace is open (portenvd does it; nothing when
    /// there's none). Asked again on the next round until portenvd can
    /// answer. A clone with the owner's sign-in reads it from the Keychain
    /// only when portenvd asks for it, and only for that call; a failed one
    /// waits for Try Again.
    private func applyKeptTeam() async {
        settingUp = true
        defer { settingUp = false }
        do {
            var r = try await daemon.applyTeamPlan(box, githubToken: nil)
            if r.needsToken {
                guard let token = try? await github.token(), !token.isEmpty else {
                    teamPlanChecked = true
                    setupProblem = SetupProblem(line: ProjectText.needsSignIn, needsSignIn: true)
                    return
                }
                r = try await daemon.applyTeamPlan(box, githubToken: token)
            }
            teamPlanChecked = true
            if let line = r.projectError, !line.isEmpty {
                setupProblem = SetupProblem(line: line, needsSignIn: r.needsSignIn)
                return
            }
            setupProblem = nil
            setupLines = r.lines
        } catch let e as DaemonError where e.message == DaemonError.outOfDate {
            teamPlanChecked = true
        } catch {}
    }

    /// The window's Try Again: portenvd still has the choice.
    public func retrySetup() async {
        setupProblem = nil
        teamPlanChecked = false
        await applyKeptTeam()
    }

    /// The Team section's Raise, after the owner confirmed it (R-0347):
    /// Agents allowed goes to `n` through portenvd, which then sends the
    /// new limit in WatchTeam. A refusal is the section's error line.
    public func raiseAgentLimit(to n: Int) async {
        do {
            _ = try await AgentsAllowed.set(n, daemon: daemon)
            limitError = nil
        } catch let e as DaemonError {
            limitError = e.message
        } catch {
            limitError = error.localizedDescription
        }
    }

    /// Why the last Raise was refused; shown under the limit's line.
    public private(set) var limitError: String?

    /// The owner's message to an agent. Nothing is sent when it's empty.
    public func send(_ text: String, to agent: String, thread: String = "") async {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        await perform {
            let m = try await self.daemon.sendMessage(self.box, to: agent, thread: thread, text: text)
            self.state.apply(.message(m))
        }
    }

    /// What the owner has typed in each agent's message field, by agent id;
    /// kept while another agent is selected, and after a refusal.
    public private(set) var drafts: [String: String] = [:]
    /// Why the last send to an agent was refused, one plain line shown
    /// under its field; cleared when the owner types or a send works.
    public private(set) var refusals: [String: String] = [:]
    private var sending: Set<String> = []

    public func draft(for agent: String) -> String { drafts[agent] ?? "" }

    public func setDraft(_ text: String, for agent: String) {
        guard drafts[agent] != text else { return }
        drafts[agent] = text
        refusals[agent] = nil
    }

    /// The message field's Return (GUIDELINES 6.2): sends exactly what was
    /// typed, as the owner, to this agent only, through portenvd's
    /// SendMessage (which wakes an idle agent). Nothing is sent when it's
    /// only white space, over 8 KB (refused here, saying so) or while a send
    /// to this agent is under way. A refusal keeps the draft; a send that
    /// worked clears it. True when it was sent.
    @discardableResult
    public func submit(to agent: String) async -> Bool {
        let text = draft(for: agent)
        guard !text.allSatisfy(\.isWhitespace), !sending.contains(agent) else { return false }
        if let refusal = TeamMessageText.refusal(text) {
            refusals[agent] = refusal
            return false
        }
        sending.insert(agent)
        defer { sending.remove(agent) }
        do {
            let m = try await daemon.sendMessage(box, to: agent, thread: "", text: text)
            state.apply(.message(m))
            if drafts[agent] == text { drafts[agent] = "" }
            refusals[agent] = nil
            return true
        } catch let e as DaemonError {
            refusals[agent] = e.message
        } catch {
            refusals[agent] = error.localizedDescription
        }
        return false
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

    /// Asks the terminal to take the keys: Show on a permission prompt
    /// (R-0412 item 18). The window's terminal watches the count.
    public private(set) var terminalFocusRequests = 0

    public func requestTerminalFocus() { terminalFocusRequests += 1 }

    /// Show on a permission prompt's row or notification: the agent's
    /// column, whose tab the window then shows (PermissionShow), with the
    /// terminal taking the keys, so the owner can answer it there.
    public func showPermission(agent id: String) {
        selection = .agent(id)
        requestTerminalFocus()
    }

    /// Tests: an event as if from the stream.
    func receive(_ event: TeamEvent) {
        state.apply(event)
        didChange()
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
