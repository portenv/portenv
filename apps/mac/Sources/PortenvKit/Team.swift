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
    /// What it says it's working on (R-0256); nil when it hasn't said.
    public var workStatus: AgentWorkStatus?
    /// Its routines (R-0258), and why they're off, if they are.
    public var routines: [AgentRoutine]
    public var routinesOff: String
    /// Set when its file asks for share_login, which this build doesn't
    /// support: nothing is shared (R-0323).
    public var shareLoginOff: String

    public init(id: String, name: String, role: String = "", avatar: String = "", remote: Bool = false, via: String = "",
                status: AgentStatus, problem: String = "", fileSHA256: String = "", approved: Bool = true,
                tabIDs: [String] = [], lastLine: String = "", pendingDecisions: Int = 0,
                homeBytes: Int64 = 0, homeQuotaBytes: Int64 = 0, runner: String = "", grant: TeamGrant = TeamGrant(),
                workStatus: AgentWorkStatus? = nil, routines: [AgentRoutine] = [], routinesOff: String = "",
                shareLoginOff: String = "") {
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
        self.workStatus = workStatus
        self.routines = routines
        self.routinesOff = TeamText.line(routinesOff, max: 300)
        self.shareLoginOff = TeamText.line(shareLoginOff, max: 300)
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
    /// For Portenv's notices about one agent (a start that failed): its
    /// id, so the notice shows in that agent's column (R-0302).
    public var about: String

    public init(id: String, from: String, to: String, threadID: String = "", kind: Kind = .answer, text: String, createdAt: Date? = nil,
                about: String = "") {
        self.id = id
        self.about = about
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
    /// For the team's first start: the agents Approve starts, in
    /// portenvd, in the order the owner chose Start (R-0296).
    public var startAgents: [String]
    /// An ask-owner's choices, in order (CC-0501): one button each, in
    /// place of Approve. Empty for a yes/no question. As portenvd sent
    /// them, because the answer must name the label exactly; the card
    /// shows `shownChoices`.
    public var choices: [String]
    /// The owner's choice, counting from 1; 0 when none.
    public var choice: Int

    public init(id: String, from: String, kind: Kind, summary: String, detail: String = "", state: State = .pending,
                answerNote: String = "", createdAt: Date? = nil, answeredAt: Date? = nil, expiresAt: Date? = nil,
                packages: [Package] = [], startAgents: [String] = [], choices: [String] = [], choice: Int = 0) {
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
        self.startAgents = startAgents.map { TeamText.line($0, max: 64) }
        self.choices = Array(choices.prefix(TeamText.maxChoices))
        self.choice = choice
    }

    /// Each choice as the card shows it: plain text on one line, at most
    /// 80 characters.
    public var shownChoices: [String] { choices.map { TeamText.line($0, max: TeamText.maxChoiceCharacters) } }

    /// The choice the owner made, as shown; nil when none.
    public var chosen: String? {
        state == .approved && choice >= 1 && choice <= choices.count ? shownChoices[choice - 1] : nil
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
    /// The team against the owner's agent limit (R-0347).
    case limit(TeamLimit)
}

/// The team as the app knows it: the stream's events folded in.
public struct TeamState: Equatable, Sendable {
    /// The agents in the order they first came (the agent files' order).
    public private(set) var agents: [TeamAgent] = []
    /// Every message, oldest first, each once.
    public private(set) var messages: [TeamMessage] = []
    /// Every decision, by first appearance, at its latest state.
    public private(set) var decisions: [TeamDecision] = []
    /// The team against the owner's agent limit (R-0347); nil from a
    /// portenvd that doesn't send it.
    public private(set) var limit: TeamLimit?
    /// Where the stream got to.
    public private(set) var cursor = ""

    public init() {}

    /// An agent is running in any way (R-0358: Delete Workspace… waits).
    public var agentsRunning: Bool {
        agents.contains { [.starting, .running, .idle, .needsYou, .connected].contains($0.status) }
    }

    public mutating func apply(_ event: TeamEvent, cursor: String = "") {
        switch event {
        case .agent(let a):
            if let i = agents.firstIndex(where: { $0.id == a.id }) { agents[i] = a } else { agents.append(a) }
            settleNeedsYou(a.id)
        case .removed(let id):
            agents.removeAll { $0.id == id }
        case .message(let m):
            if !messages.contains(where: { $0.id == m.id }) { messages.append(m) }
        case .decision(let d):
            if let i = decisions.firstIndex(where: { $0.id == d.id }) { decisions[i] = d } else { decisions.append(d) }
            settleNeedsYou(d.from)
        case .limit(let l):
            limit = l
        }
        if !cursor.isEmpty { self.cursor = cursor }
    }

    /// "Needs you" is the app's (GUIDELINES §6): a running agent shows it
    /// exactly while one of its decisions waits here, as portenvd decides
    /// it, so answering or an expiry clears it at once, and a row portenvd
    /// sent before the answer never brings it back. An agent's own status
    /// line (`portenv status set`, even `--blocked`) is its words, shown as
    /// its line: it never sets or keeps the badge.
    private mutating func settleNeedsYou(_ id: String) {
        guard let i = agents.firstIndex(where: { $0.id == id }) else { return }
        let pending = decisions.count { $0.from == id && $0.answerable }
        agents[i].pendingDecisions = pending
        switch agents[i].status {
        case .needsYou where pending == 0: agents[i].status = .running
        case .running where pending > 0: agents[i].status = .needsYou
        default: break
        }
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
        messages.filter {
            ($0.from == id && $0.to == TeamText.owner) || ($0.from == TeamText.owner && $0.to == id)
                || ($0.from == TeamText.portenv && $0.to == TeamText.owner && $0.about == id)
        }
    }

    /// An agent's decisions, newest last.
    public func decisions(from id: String) -> [TeamDecision] { decisions.filter { $0.from == id } }
}

/// The team's words and the plain-text rule.
public enum TeamText {
    /// The owner's id on the board.
    public static let owner = "owner"
    /// Portenv's own name on the board, for its notices.
    public static let portenv = "portenv"
    public static let team = "Team"
    public static let you = "You"
    public static let addAgent = "Add Agent\u{2026}"
    public static let allTabs = "All tabs"
    public static let approve = "Approve"
    public static let decline = "Decline\u{2026}"
    public static let answeredOnlyHere = "Answered only here, by you. Agents can ask but never approve."
    /// An ask-owner's choices (CC-0501): at most 4, each at most 80
    /// characters, as portenvd checks them.
    public static let maxChoices = 4
    public static let maxChoiceCharacters = 80

    /// A choice button's VoiceOver label: what it answers, as Approve's
    /// says ("Approve: merge #42").
    public static func choose(_ label: String, summary: String) -> String { "Choose \u{201C}\(label)\u{201D}: \(summary)" }

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

    /// The row's line: portenvd's reason when it can't start, what to
    /// approve, the status it set (R-0256) while it runs, or else its
    /// latest board line. `stale` is TeamStatusText's.
    public static func rowLine(_ a: TeamAgent, now: Date) -> (text: String, stale: Bool) {
        if a.status == .invalid { return (a.problem, false) }
        if let failed = startFailure(a) { return (failed, false) }
        if a.lastLine.isEmpty && !a.approved && !a.remote { return (approval(a, review: nil), false) }
        if let s = a.workStatus, [.running, .idle, .needsYou].contains(a.status) {
            return TeamStatusText.line(s, since: nil, now: now)
        }
        return (a.lastLine, false)
    }

    /// Why its last start failed, "Couldn't start: …", while it's stopped
    /// and until a start works (R-0302); shown in red in its row and under
    /// Start. Nil otherwise.
    public static func startFailure(_ a: TeamAgent) -> String? {
        a.status == .stopped && !a.problem.isEmpty ? a.problem : nil
    }

    /// The column's routines line (R-0258): why they're off, or
    /// "Routines: every 30 min"; nil when the agent has none.
    public static func routinesLine(_ a: TeamAgent) -> String? {
        a.routinesOff.isEmpty ? TeamStatusText.routines(a.routines) : a.routinesOff
    }

    /// The row as VoiceOver reads it: "Reviewer, needs you. Wants to merge
    /// #42".
    public static func spoken(_ a: TeamAgent, now: Date = Date()) -> String {
        let line = rowLine(a, now: now).text
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
            case 0x061C, 0x200E, 0x200F, 0x202A ... 0x202E, 0x2066 ... 0x2069: continue
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

/// What an agent says it's doing (R-0256): `portenv status set`, one plain
/// line, with optional progress and a blocked reason.
public struct AgentWorkStatus: Equatable, Sendable {
    public var text: String
    /// Done and total, from `--progress N/M`.
    public var progress: (done: Int, total: Int)?
    /// Why it's blocked, from `--blocked "<reason>"`.
    public var blocked: String
    public var setAt: Date?

    public init(text: String, progress: (done: Int, total: Int)? = nil, blocked: String = "", setAt: Date? = nil) {
        self.text = TeamText.line(text, max: 200)
        self.progress = progress.flatMap { $0.total > 0 && $0.done >= 0 ? (min($0.done, $0.total), $0.total) : nil }
        self.blocked = TeamText.line(blocked, max: 200)
        self.setAt = setAt
    }

    public static func == (a: Self, b: Self) -> Bool {
        a.text == b.text && a.progress?.done == b.progress?.done && a.progress?.total == b.progress?.total
            && a.blocked == b.blocked && a.setAt == b.setAt
    }
}

/// A routine from the agent's file (R-0257, R-0258): Portenv delivers its
/// note on this schedule while the agent runs.
public struct AgentRoutine: Equatable, Sendable {
    public var every: TimeInterval
    public var note: String
    public var paused: Bool

    public init(every: TimeInterval, note: String, paused: Bool = false) {
        self.every = every
        self.note = TeamText.line(note, max: 200)
        self.paused = paused
    }
}

/// The status line and routine words for an agent's row and menu.
public enum TeamStatusText {
    /// A status older than this reads as no status (R-0256).
    public static let stale: TimeInterval = 30 * 60

    /// The row's line: "Running the payments tests (3/5) · 4 min ago",
    /// "blocked: needs the staging key · 2 min ago", or, with no status or a
    /// stale one, "no status for 35 min" (`stale` true, shown secondary).
    /// `since` is when the agent started, for an agent that never set one.
    public static func line(_ s: AgentWorkStatus?, since started: Date?, now: Date) -> (text: String, stale: Bool) {
        if let s, let at = s.setAt, now.timeIntervalSince(at) <= stale, !(s.text.isEmpty && s.blocked.isEmpty) {
            var text = s.blocked.isEmpty ? s.text : "blocked: \(s.blocked)"
            if s.blocked.isEmpty, let p = s.progress { text += " (\(p.done)/\(p.total))" }
            return ("\(text) \u{00B7} \(ago(at, now: now))", false)
        }
        guard let from = s?.setAt ?? started else { return ("no status", true) }
        return ("no status for \(span(now.timeIntervalSince(from)))", true)
    }

    /// "just now", "4 min ago", "2 hours ago".
    public static func ago(_ at: Date, now: Date) -> String {
        let s = max(0, now.timeIntervalSince(at))
        return s < 60 ? "just now" : "\(span(s)) ago"
    }

    /// "35 min", "1 hour", "3 hours", "1 hour 30 min".
    public static func span(_ seconds: TimeInterval) -> String {
        let minutes = max(1, Int(seconds / 60))
        guard minutes >= 60 else { return "\(minutes) min" }
        let h = minutes / 60, m = minutes % 60
        let hours = h == 1 ? "1 hour" : "\(h) hours"
        return m == 0 ? hours : "\(hours) \(m) min"
    }

    /// The row menu's line: "Routines: every 30 min", "Routines: every
    /// 30 min, every 2 hours", "Routines: every 30 min (paused)"; nil when
    /// the agent has none.
    public static func routines(_ rs: [AgentRoutine]) -> String? {
        guard !rs.isEmpty else { return nil }
        let each = rs.map { "every \(span($0.every))" }.joined(separator: ", ")
        return rs.allSatisfy(\.paused) ? "Routines: \(each) (paused)" : "Routines: \(each)"
    }

    public static func pauseRoutines(_ paused: Bool) -> String { paused ? "Resume Routines" : "Pause Routines" }
    public static let askForStatus = "Ask for Status"
}

/// A tab's label in the tab bar (R-0265 item 4): an agent's tab always
/// carries its agent's chip and name before the tab's, so it can never pass
/// for one of the owner's tabs, whatever the agent names it ("shell").
public struct TabLabel: Equatable, Sendable {
    public var title: String
    /// The agent's chip; nil for the owner's own tabs.
    public var chip: AvatarLook?

    public static func of(_ tab: TabInfo, team: [TeamAgent], isSymbol: (String) -> Bool) -> TabLabel {
        guard !tab.agentID.isEmpty else { return TabLabel(title: tab.name, chip: nil) }
        let agent = team.first { $0.id == tab.agentID }
        let shown = agent.map { $0.name.isEmpty ? tab.agentID : $0.name } ?? tab.agentID
        // An agent the team doesn't list (yet) still gets a chip that isn't
        // the owner's: its id's initial on the first agent colour.
        let chip = agent.map { AvatarLook.agent($0, in: team, isSymbol: isSymbol) }
            ?? AvatarLook(symbol: nil, initial: tab.agentID.first.map { String($0).uppercased() } ?? "?", colour: AgentColours.order[0])
        return TabLabel(title: "\(shown) \u{00B7} \(tab.name)", chip: chip)
    }
}

/// What VoiceOver reads for the terminal (R-0314): whose tab it shows and
/// the tab's name, as the tab bar and the sidebar name them, so the
/// selection and the terminal can be checked against each other: "Terminal:
/// Lead · lead", "Terminal: You · shell".
public enum TerminalLabel {
    public static func of(_ tab: TabInfo, team: [TeamAgent]) -> String {
        "Terminal: \(title(tab, team: team))"
    }

    /// The tab's owner and name: an agent's as its tab shows it (its name
    /// and the tab's), the owner's as "You · shell".
    public static func title(_ tab: TabInfo, team: [TeamAgent]) -> String {
        tab.agentID.isEmpty ? "You \u{00B7} \(tab.name)" : TabLabel.of(tab, team: team) { _ in false }.title
    }

    /// The terminal while the selection has no tab to show: never another
    /// agent's pane.
    public static func noTab(_ selection: TeamSelection, team: [TeamAgent]) -> String {
        if case .agent(let id) = selection {
            let name = team.first { $0.id == id }.map { $0.name.isEmpty ? id : $0.name } ?? id
            return "\(name) has no tab open"
        }
        return "No tab open"
    }
}
