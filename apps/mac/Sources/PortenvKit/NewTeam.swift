// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

// New workspace's Team and Review and approve steps (ADR 0025 §3, §4).
// portenvd makes every agent file from a built-in template and the owner's
// choices (PlanTeam); the app shows exactly those bytes, and the owner
// approves each by its SHA-256 after reading it. CreateBox gets the choice
// with those hashes, and portenvd writes and pins only those bytes. The
// team's first start still asks the owner, in the workspace (ADR 0022).

/// One agent file as portenvd would write it.
public struct PlannedAgentFile: Equatable, Sendable, Identifiable {
    public var id: String
    public var name, role, avatar, templateName: String
    public var included, templateIncluded: Bool
    /// .portenv/agents/<id>.md, or .portenv/agents/off/<id>.md when left out.
    public var path: String
    public var content: String
    public var sha256: String
    public var runner, runnerName, runnerLine: String
    /// The --model value; "" for the runner's own default.
    public var model, templateModel: String
    public var models: [String]
    public var templateTeamStatus, teamStatus: Bool
    public var changes: [String]
    public var grants: [String]
    public var instructions: String
    public var problems: [String]

    public init(id: String, name: String, role: String, avatar: String, templateName: String, included: Bool, templateIncluded: Bool,
                path: String, content: String, sha256: String, runner: String, runnerName: String, runnerLine: String, model: String,
                templateModel: String, models: [String], templateTeamStatus: Bool, teamStatus: Bool, changes: [String], grants: [String],
                instructions: String, problems: [String]) {
        self.id = id
        self.name = name
        self.role = role
        self.avatar = avatar
        self.templateName = templateName
        self.included = included
        self.templateIncluded = templateIncluded
        self.path = path
        self.content = content
        self.sha256 = sha256
        self.runner = runner
        self.runnerName = runnerName
        self.runnerLine = runnerLine
        self.model = model
        self.templateModel = templateModel
        self.models = models
        self.templateTeamStatus = templateTeamStatus
        self.teamStatus = teamStatus
        self.changes = changes
        self.grants = grants
        self.instructions = instructions
        self.problems = problems
    }

    /// Whether the app shows exactly the bytes portenvd hashed: anything
    /// else can't be approved.
    public var shownExactly: Bool { SHA256Text.of(content) == sha256.lowercased() }
}

/// A built-in team as the picker shows it.
public struct TeamTemplateInfo: Equatable, Sendable, Identifiable {
    public var id, name, description: String
    public var agents: [PlannedAgentFile]
    public init(id: String, name: String, description: String, agents: [PlannedAgentFile]) {
        self.id = id
        self.name = name
        self.description = description
        self.agents = agents
    }
}

/// The owner's adjustment of one agent.
public struct TeamAgentChoice: Equatable, Sendable {
    public var id: String
    public var include: Bool
    /// "" keeps the template's.
    public var name: String
    public var model: String
    public var teamStatus: Bool
    /// The SHA-256 of the file the owner approved ("" until then).
    public var approvedSHA256: String
    public init(id: String, include: Bool, name: String = "", model: String = "", teamStatus: Bool = false, approvedSHA256: String = "") {
        self.id = id
        self.include = include
        self.name = name
        self.model = model
        self.teamStatus = teamStatus
        self.approvedSHA256 = approvedSHA256
    }
}

/// A template with the owner's choices.
public struct TeamChoice: Equatable, Sendable {
    public var template: String
    public var agents: [TeamAgentChoice]
    public init(template: String, agents: [TeamAgentChoice]) {
        self.template = template
        self.agents = agents
    }
}

/// PlanTeam's answer.
public struct TeamPlan: Equatable, Sendable {
    public var agents: [PlannedAgentFile]
    public var included: Int
    /// Settings › Team › Agents allowed.
    public var allowed: Int
    public var problems: [String]
    public init(agents: [PlannedAgentFile], included: Int, allowed: Int, problems: [String]) {
        self.agents = agents
        self.included = included
        self.allowed = allowed
        self.problems = problems
    }
}

/// portenvd's calls for the Team step.
public protocol TeamPlanning: Sendable {
    func teamTemplates() async throws -> [TeamTemplateInfo]
    func planTeam(_ choice: TeamChoice) async throws -> TeamPlan
    func agentsAllowed() async throws -> Int
    /// Only after the owner confirmed it (R-0347).
    func setAgentsAllowed(_ n: Int) async throws -> Int
}

/// ApplyTeamPlan's answer.
public struct TeamPlanApplied: Equatable, Sendable {
    public var applied: Bool
    public var pinned: [String]
    public var lines: [String]
    /// The project couldn't be brought in: one plain line; nothing was
    /// written, and portenvd keeps the choice for Try Again.
    public var projectError: String?
    public var needsSignIn: Bool
    /// The clone needs the owner's GitHub sign-in, which the app reads.
    public var needsToken: Bool
    public init(applied: Bool, pinned: [String] = [], lines: [String] = [], projectError: String? = nil, needsSignIn: Bool = false, needsToken: Bool = false) {
        self.applied = applied
        self.pinned = pinned
        self.lines = lines
        self.projectError = projectError
        self.needsSignIn = needsSignIn
        self.needsToken = needsToken
    }
}

/// The window's line when the project couldn't be brought in.
public struct SetupProblem: Equatable, Sendable {
    public var line: String
    /// Sign in with GitHub would fix it.
    public var needsSignIn: Bool
    public init(line: String, needsSignIn: Bool) {
        self.line = line
        self.needsSignIn = needsSignIn
    }
}

/// The Team and Review steps' state.
@MainActor
@Observable
public final class NewTeamFlow {
    /// Where an agent's file stands in the review.
    public enum ReviewState: Equatable, Sendable {
        case notOpened, opened, approved
        /// Opened or approved before, and the file has changed since.
        case changed
    }

    public private(set) var templates: [TeamTemplateInfo] = []
    /// nil: None, just the owner.
    public private(set) var template: String?
    /// Every agent of the chosen template, as it would be written.
    public private(set) var agents: [PlannedAgentFile] = []
    public private(set) var problems: [String] = []
    public private(set) var allowed = AgentsAllowed.defaultValue
    /// A plan or a raise portenvd refused, one plain line.
    public private(set) var error: String?
    /// The agent on show in the review.
    public var reviewing: String?

    private var choices: [String: TeamAgentChoice] = [:]
    /// The SHA-256 each agent's file had when it was opened, and approved.
    private var openedSHA: [String: String] = [:]
    private var approvedSHA: [String: String] = [:]
    /// Opened at some point (for "changed").
    private var seen: Set<String> = []
    private var generation = 0
    private let planner: TeamPlanning

    public init(planner: TeamPlanning) {
        self.planner = planner
    }

    /// The templates and the owner's limit.
    public func load() async {
        do {
            templates = try await planner.teamTemplates()
            allowed = try await planner.agentsAllowed()
            error = nil
        } catch {
            self.error = Self.line(error)
        }
    }

    public var included: [PlannedAgentFile] { agents.filter(\.included) }

    /// Picks a team (nil: None). Every choice starts from the template's.
    public func choose(_ id: String?) async {
        template = id
        choices = [:]
        openedSHA = [:]
        approvedSHA = [:]
        seen = []
        reviewing = nil
        agents = templates.first { $0.id == id }?.agents ?? []
        problems = []
        error = nil
        for a in agents {
            choices[a.id] = TeamAgentChoice(id: a.id, include: a.templateIncluded, name: a.name, model: a.model, teamStatus: a.teamStatus)
        }
        if id != nil { await replan() }
    }

    public func setIncluded(_ id: String, _ on: Bool) async { await change(id) { $0.include = on } }
    public func rename(_ id: String, _ name: String) async { await change(id) { $0.name = name } }
    public func setModel(_ id: String, _ model: String) async { await change(id) { $0.model = model } }
    public func setTeamStatus(_ id: String, _ on: Bool) async { await change(id) { $0.teamStatus = on } }
    /// The review's Leave Out.
    public func leaveOut(_ id: String) async {
        await setIncluded(id, false)
        if reviewing == id { reviewing = included.first?.id }
    }

    /// The name as the owner typed it, for the field.
    public func typedName(_ id: String) -> String { choices[id]?.name ?? "" }

    private func change(_ id: String, _ edit: (inout TeamAgentChoice) -> Void) async {
        guard var c = choices[id] else { return }
        edit(&c)
        choices[id] = c
        await replan()
    }

    private func currentChoice() -> TeamChoice? {
        guard let template else { return nil }
        let order = agents.map(\.id)
        let list = order.compactMap { choices[$0] }.map { c -> TeamAgentChoice in
            var c = c
            c.name = c.name.trimmingCharacters(in: .whitespaces)
            return c
        }
        return TeamChoice(template: template, agents: list)
    }

    private func replan() async {
        guard let choice = currentChoice() else { return }
        generation += 1
        let mine = generation
        do {
            let plan = try await planner.planTeam(choice)
            guard mine == generation else { return } // a newer change is on its way
            agents = plan.agents
            problems = plan.problems
            allowed = plan.allowed
            error = nil
            // A file that's no longer what was opened or approved goes back
            // to changed, not opened.
            for a in plan.agents {
                if let o = openedSHA[a.id], o != a.sha256 { openedSHA[a.id] = nil }
                if let p = approvedSHA[a.id], p != a.sha256 { approvedSHA[a.id] = nil }
            }
            if let r = reviewing, !included.contains(where: { $0.id == r }) { reviewing = included.first?.id }
        } catch {
            guard mine == generation else { return }
            self.error = Self.line(error)
        }
    }

    // MARK: The Team step

    /// The one-click raise when the team needs more agents than allowed.
    public var raise: TeamLimit? {
        let n = included.count
        guard template != nil, n > allowed, n <= AgentsAllowed.range.upperBound else { return nil }
        return TeamLimit(allowed: allowed, inTeam: n, running: 0, line: problems.first ?? "", raiseTo: n)
    }

    /// The Raise, after the owner confirmed it: Settings › Team › Agents
    /// allowed through portenvd, then the plan again.
    public func raiseLimit() async {
        guard let raise else { return }
        do {
            allowed = try await planner.setAgentsAllowed(raise.raiseTo)
            await replan()
        } catch {
            self.error = Self.line(error)
        }
    }

    public var canContinue: Bool { whyNotContinue == nil }

    public var whyNotContinue: String? {
        guard template != nil else { return nil }
        if let error { return error }
        if included.isEmpty { return NewTeamText.noAgents }
        if let p = problems.first { return p }
        if let bad = included.first(where: { !$0.problems.isEmpty }) { return bad.problems[0] }
        return nil
    }

    // MARK: Review and approve

    public func reviewState(_ id: String) -> ReviewState {
        guard let a = agents.first(where: { $0.id == id }) else { return .notOpened }
        if approvedSHA[id] == a.sha256 { return .approved }
        if openedSHA[id] == a.sha256 { return .opened }
        return seen.contains(id) ? .changed : .notOpened
    }

    /// The owner has read the file to its end (its instructions scrolled to
    /// the last line once).
    public func opened(_ id: String) {
        guard let a = included.first(where: { $0.id == id }), a.shownExactly else { return }
        openedSHA[id] = a.sha256
        seen.insert(id)
    }

    public func canApprove(_ id: String) -> Bool {
        guard let a = included.first(where: { $0.id == id }) else { return false }
        return a.shownExactly && a.problems.isEmpty && openedSHA[id] == a.sha256
    }

    /// Approve <Name>: pins (in the choice) exactly the file the owner read.
    public func approve(_ id: String) {
        guard canApprove(id), let a = included.first(where: { $0.id == id }) else { return }
        approvedSHA[id] = a.sha256
    }

    private var unopened: [PlannedAgentFile] { included.filter { openedSHA[$0.id] != $0.sha256 } }

    public var approveAllEnabled: Bool { !included.isEmpty && unopened.isEmpty }

    /// Under Approve All while it's off.
    public var approveAllLine: String? {
        let n = unopened.count
        return n == 0 ? nil : "Open every agent first (\(n) left)."
    }

    /// Approves exactly the files the owner opened.
    public func approveAll() {
        guard approveAllEnabled else { return }
        for a in included { approve(a.id) }
    }

    public var canCreate: Bool { whyNotCreate == nil }

    public var whyNotCreate: String? {
        if let why = whyNotContinue { return why }
        let waiting = included.filter { approvedSHA[$0.id] != $0.sha256 }.map(\.name)
        guard !waiting.isEmpty else { return nil }
        return "Approve \(NewTeamText.join(waiting)) to create the workspace."
    }

    /// What CreateBox gets: nil for None.
    public var choice: TeamChoice? {
        guard var c = currentChoice() else { return nil }
        for i in c.agents.indices {
            let id = c.agents[i].id
            if let a = included.first(where: { $0.id == id }), approvedSHA[id] == a.sha256 {
                c.agents[i].approvedSHA256 = a.sha256
            }
        }
        return c
    }

    static func line(_ error: Error) -> String {
        TeamText.line((error as? LocalizedError)?.errorDescription ?? error.localizedDescription, max: 300)
    }
}

/// The Team and Review steps' words (GUIDELINES §10).
public enum NewTeamText {
    public static let teamTitle = "Team"
    public static let teamLine = "Pick a team of agents for this workspace, then adjust it. You read and approve every agent\u{2019}s file next."
    public static let noneTitle = "None"
    public static let noneLine = "Just you. Add agents later."
    public static let noAgents = "Include at least one agent, or choose None."
    public static let runnerOnly = "The runner menu lists Anthropic \u{00B7} Claude Code for now. Leaving an agent out also takes it out of the others\u{2019} message lists."
    public static let signIn = "Agents sign in themselves, in their own tabs. Templates never carry logins."
    public static let reviewTitle = "Review and approve"
    public static let approveAll = "Approve All"
    public static let leaveOut = "Leave Out"
    public static let runs = "Runs"
    public static let itMay = "It may"
    public static let instructions = "Instructions"
    public static let changedFromTemplate = "Changed from the template:"
    /// The review's project summary (ADR 0025 §4).
    public static func projectTitle(_ p: ProjectChoice) -> String {
        switch p {
        case .empty: "Empty project"
        case .github(let url, _): url.replacingOccurrences(of: "https://", with: "")
        }
    }

    public static func projectSummary(_ p: ProjectChoice) -> String {
        switch p {
        case .empty: ProjectText.emptyLine
        case .github(_, let signIn): signIn
            ? "Cloned with your GitHub sign-in when the workspace first opens. The agents work on it, each in its own copy."
            : "Cloned when the workspace first opens. The agents work on it, each in its own copy."
        }
    }
    /// "Lead, Builder and Reviewer".
    public static func join(_ names: [String]) -> String {
        guard let last = names.last else { return "" }
        return names.count == 1 ? last : names.dropLast().joined(separator: ", ") + " and " + last
    }

    public static func model(_ m: String) -> String { m.isEmpty ? "Default" : m.prefix(1).uppercased() + m.dropFirst() }
    public static func approve(_ name: String) -> String { "Approve \(name)" }
    public static func written(_ path: String, sha: String) -> String { "Written to \(path) \u{00B7} SHA-256 \(sha)" }
    public static func teamStatus(_ name: String) -> String {
        "Let \(name) see the team\u{2019}s status (states and status lines, never terminals or files)"
    }

    public static func state(_ s: NewTeamFlow.ReviewState) -> String {
        switch s {
        case .notOpened: "Not opened"
        case .opened: "Opened"
        case .approved: "Approved"
        case .changed: "Changed"
        }
    }
}

public extension AvatarLook {
    /// A planned agent's: its initial on its colour (GUIDELINES §11), by
    /// its place in the team when its file names none of them.
    static func planned(_ a: PlannedAgentFile, index: Int) -> AvatarLook {
        let colour = AgentColours.order.contains(a.avatar) ? a.avatar : AgentColours.order[index % AgentColours.order.count]
        return AvatarLook(symbol: nil, initial: a.name.first.map { String($0).uppercased() } ?? "?", colour: colour)
    }
}

/// How many columns New workspace's choice cards take at a width: as many
/// as fit at their minimum width, in balanced rows (2 + 2, not 3 + 1), so
/// a step never runs wider than its window.
public enum CardColumns {
    public static let minimumWidth: CGFloat = 200
    public static let spacing: CGFloat = 10

    /// Never more than four in a row, however wide the window.
    public static let maximum = 4

    public static func count(cards: Int, width: CGFloat) -> Int {
        guard cards > 0 else { return 1 }
        let fit = max(1, Int(((width + spacing) / (minimumWidth + spacing)).rounded(.down)))
        let most = min(fit, maximum, cards)
        let rows = (cards + most - 1) / most
        return (cards + rows - 1) / rows
    }

    public static func columnWidth(columns: Int, width: CGFloat) -> CGFloat {
        let n = CGFloat(max(1, columns))
        return (width - (n - 1) * spacing) / n
    }
}
