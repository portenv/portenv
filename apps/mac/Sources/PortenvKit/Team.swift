// SPDX-License-Identifier: Apache-2.0

import Foundation

// The agent team (ADR 0022): the agents in a workspace, their board
// messages and the owner's decisions, as the Team sidebar shows them.
// Everything an agent wrote reaches the app as plain text and stays that
// way (§6): it's cleaned here before any view sees it.

/// Where an agent stands (ADR 0022 §8).
public enum AgentStatus: Equatable, Sendable {
    case stopped, starting, running, idle, needsYou, invalid
    /// Remote agents (§10), after the owner's run.
    case connected, disconnected
}

/// An agent's grant for panes and packages (§3).
public enum GrantMode: Equatable, Sendable { case ask, allow, never }

/// An agent's effective grant, after personal overrides (§3).
public struct TeamGrant: Equatable, Sendable {
    public var panes: GrantMode
    public var maxPanes: Int
    /// Agent ids it may message; the owner is always allowed.
    public var message: [String]
    public var askOwner: Bool
    public var packages: GrantMode

    public init(panes: GrantMode = .ask, maxPanes: Int = 4, message: [String] = [], askOwner: Bool = true, packages: GrantMode = .ask) {
        self.panes = panes
        self.maxPanes = maxPanes
        self.message = message
        self.askOwner = askOwner
        self.packages = packages
    }
}

/// One agent, as the Team sidebar shows it.
public struct TeamAgent: Equatable, Sendable, Identifiable {
    /// Also its login name in the workspace.
    public var id: String
    public var name: String
    public var role: String
    /// An SF Symbol name, or one of GUIDELINES §11's agent colours.
    public var avatar: String
    public var remote: Bool
    /// "ssh", "mcp" or "web" when remote.
    public var via: String
    public var status: AgentStatus
    /// One plain line when it's invalid or a start failed.
    public var problem: String
    public var fileSHA256: String
    public var approved: Bool
    /// Its tabs, in the order of the tab list.
    public var tabIDs: [String]
    /// Its last board line, plain text, one line.
    public var lastLine: String
    public var pendingDecisions: Int
    public var homeBytes: Int64
    public var homeQuotaBytes: Int64
    /// The runner line from its file: code that will run (R-0245 item 4).
    public var runner: String
    public var grant: TeamGrant

    public init(id: String, name: String, role: String = "", avatar: String = "", remote: Bool = false, via: String = "",
                status: AgentStatus, problem: String = "", fileSHA256: String = "", approved: Bool = true,
                tabIDs: [String] = [], lastLine: String = "", pendingDecisions: Int = 0,
                homeBytes: Int64 = 0, homeQuotaBytes: Int64 = 0, runner: String = "", grant: TeamGrant = TeamGrant()) {
        self.id = id
        self.name = TeamText.line(name, max: 64)
        self.role = TeamText.line(role, max: 120)
        self.avatar = avatar
        self.remote = remote
        self.via = via
        self.status = status
        self.problem = TeamText.line(problem, max: 300)
        self.fileSHA256 = fileSHA256
        self.approved = approved
        self.tabIDs = tabIDs
        self.lastLine = TeamText.line(lastLine, max: 200)
        self.pendingDecisions = pendingDecisions
        self.homeBytes = homeBytes
        self.homeQuotaBytes = homeQuotaBytes
        self.runner = TeamText.line(runner, max: 300)
        self.grant = grant
    }
}

/// One board message (§6).
public struct TeamMessage: Equatable, Sendable, Identifiable {
    public enum Kind: Equatable, Sendable { case question, answer, notice }

    public var id: String
    /// An agent id, "owner", or "portenv" for Portenv's own notices.
    public var from: String
    public var to: String
    public var threadID: String
    public var kind: Kind
    /// Plain text: control characters removed, line breaks kept.
    public var text: String
    public var createdAt: Date?

    public init(id: String, from: String, to: String, threadID: String = "", kind: Kind = .answer, text: String, createdAt: Date? = nil) {
        self.id = id
        self.from = from
        self.to = to
        self.threadID = threadID
        self.kind = kind
        self.text = TeamText.plain(text, max: 8 * 1024)
        self.createdAt = createdAt
    }
}

/// A request for the owner (§6). Only the app answers it.
public struct TeamDecision: Equatable, Sendable, Identifiable {
    public enum Kind: Equatable, Sendable {
        case askOwner, pane, agentFileChange, shareLogin, package, teamStart, deleteAgentFiles
    }

    public enum State: Equatable, Sendable { case pending, approved, declined, expired }

    /// One package Approve installs (R-0245 item 5).
    public struct Package: Equatable, Sendable {
        public var name: String
        public var version: String
        /// The configured repository it comes from.
        public var source: String
        public var dependency: Bool

        public init(name: String, version: String, source: String, dependency: Bool = false) {
            self.name = TeamText.line(name, max: 200)
            self.version = TeamText.line(version, max: 200)
            self.source = TeamText.line(source, max: 300)
            self.dependency = dependency
        }
    }

    public var id: String
    /// The agent that asked.
    public var from: String
    public var kind: Kind
    public var summary: String
    /// The question, the command, the diff or the package list.
    public var detail: String
    public var state: State
    public var answerNote: String
    public var createdAt: Date?
    public var answeredAt: Date?
    public var expiresAt: Date?
    public var packages: [Package]

    public init(id: String, from: String, kind: Kind, summary: String, detail: String = "", state: State = .pending,
                answerNote: String = "", createdAt: Date? = nil, answeredAt: Date? = nil, expiresAt: Date? = nil,
                packages: [Package] = []) {
        self.id = id
        self.from = from
        self.kind = kind
        self.summary = TeamText.line(summary, max: 200)
        self.detail = TeamText.plain(detail, max: 32 * 1024)
        self.state = state
        self.answerNote = TeamText.plain(answerNote, max: 8 * 1024)
        self.createdAt = createdAt
        self.answeredAt = answeredAt
        self.expiresAt = expiresAt
        self.packages = packages
    }

    /// Approve and Decline show only while it's pending; an expired
    /// decision never turns into an approval (R-0245).
    public var answerable: Bool { state == .pending }
}

/// What the team stream says, one event at a time, with the cursor to
/// resume from after it (WatchTeam).
public enum TeamEvent: Equatable, Sendable {
    case agent(TeamAgent)
    case message(TeamMessage)
    case decision(TeamDecision)
    case removed(String)
}

/// The team as the app knows it: the stream's events folded in.
public struct TeamState: Equatable, Sendable {
    /// The agents in the order they first came (the agent files' order).
    public private(set) var agents: [TeamAgent] = []
    /// Every message, oldest first, each once.
    public private(set) var messages: [TeamMessage] = []
    /// Every decision, by first appearance, at its latest state.
    public private(set) var decisions: [TeamDecision] = []
    /// Where the stream got to.
    public private(set) var cursor = ""

    public init() {}

    public mutating func apply(_ event: TeamEvent, cursor: String = "") {
        switch event {
        case .agent(let a):
            if let i = agents.firstIndex(where: { $0.id == a.id }) { agents[i] = a } else { agents.append(a) }
        case .removed(let id):
            agents.removeAll { $0.id == id }
        case .message(let m):
            if !messages.contains(where: { $0.id == m.id }) { messages.append(m) }
        case .decision(let d):
            if let i = decisions.firstIndex(where: { $0.id == d.id }) { decisions[i] = d } else { decisions.append(d) }
        }
        if !cursor.isEmpty { self.cursor = cursor }
    }

    /// Whether the workspace has a team: the sidebar shows by default when
    /// it does (R-0240).
    public var hasTeam: Bool { !agents.isEmpty }

    public func agent(_ id: String) -> TeamAgent? { agents.first { $0.id == id } }

    /// The decisions waiting for the owner, oldest first: the sidebar's
    /// "Needs you".
    public var needsYou: [TeamDecision] {
        decisions.filter(\.answerable).sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
    }

    /// An agent's conversation with the owner: what it sent the owner, what
    /// the owner sent it, and Portenv's notices to the owner about it.
    public func conversation(with id: String) -> [TeamMessage] {
        messages.filter { ($0.from == id && $0.to == TeamText.owner) || ($0.from == TeamText.owner && $0.to == id) }
    }

    /// An agent's decisions, newest last.
    public func decisions(from id: String) -> [TeamDecision] { decisions.filter { $0.from == id } }
}

/// The team's words and the plain-text rule.
public enum TeamText {
    /// The owner's id on the board.
    public static let owner = "owner"
    public static let team = "Team"
    public static let you = "You"
    public static let addAgent = "Add Agent\u{2026}"
    public static let allTabs = "All tabs"
    public static let approve = "Approve"
    public static let decline = "Decline\u{2026}"
    public static let answeredOnlyHere = "Answered only here, by you. Agents can ask but never approve."

    /// A status as a word (GUIDELINES: never colour alone).
    public static func word(_ s: AgentStatus) -> String {
        switch s {
        case .stopped: "stopped"
        case .starting: "starting"
        case .running: "running"
        case .idle: "idle"
        case .needsYou: "needs you"
        case .invalid: "can\u{2019}t start"
        case .connected: "connected"
        case .disconnected: "disconnected"
        }
    }

    /// Its symbol (SF Symbols): ▶ running, ◌ idle, ● needs you (R-0241).
    public static func symbol(_ s: AgentStatus) -> String {
        switch s {
        case .stopped: "stop.fill"
        case .starting: "hourglass"
        case .running: "play.fill"
        case .idle: "circle.dotted"
        case .needsYou, .connected: "circle.fill"
        case .invalid: "exclamationmark.triangle"
        case .disconnected: "circle"
        }
    }

    /// The row as VoiceOver reads it: "Reviewer, needs you. Wants to merge
    /// #42".
    public static func spoken(_ a: TeamAgent) -> String {
        let line = a.status == .invalid && !a.problem.isEmpty ? a.problem : a.lastLine
        return line.isEmpty ? "\(a.name), \(word(a.status))" : "\(a.name), \(word(a.status)). \(line)"
    }

    /// "Needs you · 1".
    public static func needsYouHeading(_ n: Int) -> String { "Needs you \u{00B7} \(n)" }

    /// The column's subtitle: "Runs as user builder · Claude Code · may
    /// open 4 panes" (R-0241). The runner is shown as its program's name.
    public static func runsAs(_ a: TeamAgent) -> String {
        var parts = ["Runs as user \(a.id)"]
        if let program = runnerName(a.runner) { parts.append(program) }
        switch a.grant.panes {
        case .never: parts.append("can\u{2019}t open panes")
        case .ask, .allow: parts.append(a.grant.maxPanes == 1 ? "may open 1 pane" : "may open \(a.grant.maxPanes) panes")
        }
        return parts.joined(separator: " \u{00B7} ")
    }

    /// The runner's program, by its known names; otherwise its first word.
    public static func runnerName(_ runner: String) -> String? {
        guard let first = runner.split(separator: " ").first.map(String.init), !first.isEmpty else { return nil }
        let base = (first as NSString).lastPathComponent
        let known = ["claude": "Claude Code", "codex": "Codex", "cursor-agent": "Cursor Agent"]
        return known[base] ?? base
    }

    /// "Waiting 6 min", from when it was asked.
    public static func waiting(since: Date?, now: Date) -> String? {
        guard let since else { return nil }
        let minutes = max(0, Int(now.timeIntervalSince(since) / 60))
        switch minutes {
        case 0: return "Waiting less than a minute"
        case ..<60: return "Waiting \(minutes) min"
        default:
            let h = minutes / 60
            return h == 1 ? "Waiting 1 hour" : "Waiting \(h) hours"
        }
    }

    /// "212 MB of 500 MB".
    public static func quota(used: Int64, of limit: Int64, locale: Locale = SettingsText.appLocale()) -> String {
        "\(size(used, locale: locale)) of \(size(limit, locale: locale))"
    }

    /// A size as Finder counts it (1 MB = 1,000,000 bytes), in the app's
    /// words whatever the Mac's language: "4 KB", "1.2 MB", "2 GB".
    public static func size(_ bytes: Int64, locale: Locale = SettingsText.appLocale()) -> String {
        let units: [(Double, String)] = [(1e9, "GB"), (1e6, "MB"), (1e3, "KB")]
        let b = Double(max(0, bytes))
        guard let (scale, unit) = units.first(where: { b >= $0.0 }) else { return "\(max(0, bytes)) bytes" }
        let v = b / scale
        let f = NumberFormatter()
        f.locale = locale
        f.numberStyle = .decimal
        f.maximumFractionDigits = v < 10 ? 1 : 0
        return "\(f.string(from: NSNumber(value: v)) ?? "\(Int(v))") \(unit)"
    }

    /// Plain text from an agent: no control characters or escape
    /// sequences (no ESC, no C1, no bidi overrides), tabs as spaces, line
    /// breaks kept, at most `max` characters.
    public static func plain(_ s: String, max: Int) -> String {
        var out = String.UnicodeScalarView()
        var count = 0
        for u in s.unicodeScalars {
            guard count < max else { break }
            switch u.value {
            case 0x0A: out.append(u)
            case 0x09: out.append(" ")
            case 0x00 ... 0x1F, 0x7F ... 0x9F: continue
            // Bidi controls and isolates can reorder what's shown.
            case 0x200E, 0x200F, 0x202A ... 0x202E, 0x2066 ... 0x2069: continue
            default: out.append(u)
            }
            count += 1
        }
        return String(out)
    }

    /// One line: plain, with line breaks and runs of spaces as one space.
    public static func line(_ s: String, max: Int) -> String {
        plain(s, max: max * 4)
            .split(whereSeparator: { $0 == "\n" || $0 == " " })
            .joined(separator: " ")
            .prefix(max)
            .description
    }
}

/// How an avatar looks. The owner's is their own (a person on grey), and
/// no agent's can look like it: an agent never gets grey or a person
/// symbol, whatever its file says (R-0253), so nothing an agent sets can
/// pass for the owner.
public struct AvatarLook: Equatable, Sendable {
    /// An SF Symbol, or nil for the initial.
    public var symbol: String?
    public var initial: String
    /// One of AgentColours.order, or "gray" for the owner.
    public var colour: String

    public static let owner = AvatarLook(symbol: "person.fill", initial: "", colour: "gray")

    /// An agent's: its file's SF Symbol when it names a real one that isn't
    /// a person, else its initial; its colour by team order (GUIDELINES 11).
    public static func agent(_ a: TeamAgent, in team: [TeamAgent], isSymbol: (String) -> Bool) -> AvatarLook {
        let symbol = !AgentColours.order.contains(a.avatar) && !a.avatar.hasPrefix("person") && isSymbol(a.avatar) ? a.avatar : nil
        let initial = a.name.first.map { String($0).uppercased() } ?? "?"
        return AvatarLook(symbol: symbol, initial: initial, colour: AgentColours.name(for: a, in: team))
    }
}

/// GUIDELINES §11: agent colours in this order, by team order; an avatar
/// that names one of them keeps it.
public enum AgentColours {
    public static let order = ["indigo", "teal", "purple", "pink", "brown", "mint"]

    public static func name(for agent: TeamAgent, in team: [TeamAgent]) -> String {
        if order.contains(agent.avatar) { return agent.avatar }
        let i = team.firstIndex { $0.id == agent.id } ?? 0
        return order[i % order.count]
    }
}
