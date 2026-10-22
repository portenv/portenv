// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The only action a Needs-you notification offers: Show, which brings the
/// workspace forward with the question selected. A decision is answered
/// only in the app, never from a notification (GUIDELINES §6.2, R-0384);
/// a tab's question gets the inline card's two answers here once that card
/// exists (§4.4, §5; PLAN 1.4 item 4).
public enum NotificationAction: String, CaseIterable, Sendable {
    case show
}

/// Where a notification's click goes: the decision in its workspace's
/// window, or the tab that's asking.
public enum NotificationRoute: Equatable, Sendable {
    case decision(box: String, id: String)
    case tab(box: String, id: String)
    /// An agent waiting at its runner's permission prompt (R-0412 item
    /// 18): its column, and its runner's tab with the terminal focused.
    case agent(box: String, id: String)

    public var box: String {
        switch self {
        case .decision(let box, _), .tab(let box, _), .agent(let box, _): box
        }
    }

    /// What the notification carries, as plain strings.
    public var userInfo: [String: String] {
        switch self {
        case .decision(let box, let id): ["kind": "decision", "workspace": box, "id": id]
        case .tab(let box, let id): ["kind": "tab", "workspace": box, "id": id]
        case .agent(let box, let id): ["kind": "agent", "workspace": box, "id": id]
        }
    }

    /// Read back from a notification; nil for anything else.
    public init?(userInfo: [AnyHashable: Any]) {
        guard let kind = userInfo["kind"] as? String, let box = userInfo["workspace"] as? String, !box.isEmpty,
              let id = userInfo["id"] as? String, !id.isEmpty else { return nil }
        switch kind {
        case "decision": self = .decision(box: box, id: id)
        case "tab": self = .tab(box: box, id: id)
        case "agent": self = .agent(box: box, id: id)
        default: return nil
        }
    }

    /// What the Team sidebar selects for a decision, as its Needs-you row
    /// does: the asking agent's column, or the decision on its own when
    /// Portenv asks. Nil once it's no longer waiting.
    public static func selection(decision id: String, in team: TeamState) -> TeamSelection? {
        guard let d = team.needsYou.first(where: { $0.id == id }) else { return nil }
        return team.agent(d.from) == nil ? .decision(d.id) : .agent(d.from)
    }
}

/// One notification for something that needs the owner (§5): a decision
/// an agent asks, or a tab's program waiting for input.
public struct NeedsYouNotice: Equatable, Sendable {
    /// Stable per question: a newer state replaces it, never stacks.
    public let id: String
    public let content: NotifyText.Content
    public let route: NotificationRoute
    public let category: String
    public let actions: [NotificationAction]

    public static let decisionCategory = "needs-you-decision"
    public static let waitingCategory = "needs-you-waiting"

    /// A decision waiting for the owner. Show only, whatever it asks.
    public static func decision(_ d: TeamDecision, box: String, team: TeamState, location: String?) -> NeedsYouNotice {
        let who = asker(d.from, team: team.agents)
        return NeedsYouNotice(
            id: "decision-\(box)-\(d.id)",
            content: NotifyText.Content(title: "\(box) \u{00B7} \(who) is asking",
                                        body: TeamText.line(d.summary, max: 200),
                                        subtitle: "\(who) \u{00B7} \(location ?? NotifyText.thisMac)"),
            route: .decision(box: box, id: d.id),
            category: decisionCategory,
            actions: [.show])
    }

    /// A tab whose program waits for input; nil when it doesn't. A secret
    /// prompt says only that it asks: its text never leaves the terminal.
    public static func waiting(_ tab: TabInfo, box: String, team: [TeamAgent], location: String?) -> NeedsYouNotice? {
        guard let q = tab.waiting else { return nil }
        let who: String
        if !tab.agentID.isEmpty {
            who = asker(tab.agentID, team: team)
        } else if let program = TeamText.runnerName(tab.program) {
            who = program
        } else {
            who = "A program"
        }
        let tabName = TeamText.line(tab.name, max: 64)
        return NeedsYouNotice(
            id: "waiting-\(box)-\(tab.id)",
            content: NotifyText.Content(title: "\(box) \u{00B7} \(who) is asking",
                                        body: q.secret ? NotifyText.secretPrompt : TeamText.line(q.text, max: 200),
                                        subtitle: "In the \(tabName) tab \u{00B7} \(location ?? NotifyText.thisMac)"),
            route: .tab(box: box, id: tab.id),
            category: waitingCategory,
            actions: [.show])
    }

    /// An agent waiting at its runner's permission prompt (R-0412 item
    /// 18); nil when it isn't. Only the one-line summary, never the
    /// command's whole text. Show only: the owner answers in the terminal.
    public static func permission(_ a: TeamAgent, box: String, location: String?) -> NeedsYouNotice? {
        guard a.permission != nil else { return nil }
        let who = a.name.isEmpty ? TeamText.line(a.id, max: 64) : a.name
        return NeedsYouNotice(
            id: "permission-\(box)-\(a.id)",
            content: NotifyText.Content(title: "\(box) \u{00B7} \(who) is asking",
                                        body: TeamText.waitingForPermission(a),
                                        subtitle: "\(who) \u{00B7} \(location ?? NotifyText.thisMac)"),
            route: .agent(box: box, id: a.id),
            category: waitingCategory,
            actions: [.show])
    }

    private static func asker(_ id: String, team: [TeamAgent]) -> String {
        if id == TeamText.portenv { return "Portenv" }
        return team.first { $0.id == id }?.name ?? TeamText.line(id, max: 64)
    }
}

public extension NotifyText {
    static let thisMac = "This Mac"
    /// A password prompt's body: its text stays in the terminal.
    static let secretPrompt = "It\u{2019}s asking for a password. Answer it in the terminal."
}

/// The Dock badge: every open workspace's Needs you, added up; none at 0.
public enum DockBadge {
    public static func label(_ count: Int) -> String? { count > 0 ? "\(count)" : nil }
}

/// What a Needs-you notification needs from the system.
@MainActor
public protocol NeedsYouPosting: AnyObject {
    func post(_ n: NeedsYouNotice)
    /// Takes back delivered notifications by id.
    func withdraw(_ ids: [String])
    func setBadge(_ label: String?)
}

/// Turns each open session's team and tabs into notifications and the Dock
/// badge (§5, basics audit rows 13 and 14). Each workspace window's session
/// reports its own workspace, so a workspace in a background tab notifies
/// the same as one in front of another app.
@MainActor
public final class NeedsYouCenter {
    private let poster: NeedsYouPosting
    private let inFront: (String) -> Bool
    private let now: () -> Date
    /// Per workspace: when its session started following, the decisions
    /// already seen, and the tabs' questions as last notified.
    private var since: [String: Date] = [:]
    private var seen: [String: Set<String>] = [:]
    private var posted: [String: Set<String>] = [:]
    private var questions: [String: [String: TabQuestion]] = [:]
    /// Per workspace: each waiting agent's prompt as last notified.
    private var waits: [String: [String: PermissionPrompt]] = [:]
    private var counts: [String: Int] = [:]
    private var badge: String?

    /// Decisions asked up to this long before the session started
    /// following still count as new (the stream's first events).
    public static let grace: TimeInterval = 5

    public init(poster: NeedsYouPosting, inFront: @escaping (String) -> Bool, now: @escaping () -> Date = { Date() }) {
        self.poster = poster
        self.inFront = inFront
        self.now = now
    }

    /// Everything waiting, across the open sessions.
    public var count: Int { counts.values.reduce(0, +) }

    /// A session's team changed. New decisions notify unless the
    /// workspace's window is in front (it shows them there); answered,
    /// expired or gone ones are taken back.
    public func team(box: String, state: TeamState, location: String?) {
        let started = since[box] ?? now()
        since[box] = started
        let pending = state.needsYou
        let ids = Set(pending.map(\.id))
        var known = seen[box] ?? []
        var out = posted[box] ?? []
        let front = inFront(box)
        for d in pending where !known.contains(d.id) {
            known.insert(d.id)
            let asked = d.createdAt ?? now()
            guard !front, asked >= started.addingTimeInterval(-Self.grace) else { continue }
            let n = NeedsYouNotice.decision(d, box: box, team: state, location: location)
            poster.post(n)
            out.insert(n.id)
        }
        let done = known.subtracting(ids)
        let withdraw = done.map { "decision-\(box)-\($0)" }.filter { out.contains($0) }.sorted()
        if !withdraw.isEmpty { poster.withdraw(withdraw) }
        out.subtract(withdraw)
        seen[box] = known.intersection(ids)
        posted[box] = out
        permissions(box: box, state: state, front: front, location: location)
        counts[box] = state.needsYouCount
        updateBadge()
    }

    /// Permission prompts (R-0412 item 18): a new or different one
    /// notifies, replacing the agent's last; one that's over is taken back.
    private func permissions(box: String, state: TeamState, front: Bool, location: String?) {
        var last = waits[box] ?? [:]
        let now = Dictionary(uniqueKeysWithValues: state.permissionWaits.compactMap { a in a.permission.map { (a.id, $0) } })
        let over = last.keys.filter { now[$0] == nil }.sorted()
        for id in over { last[id] = nil }
        if !over.isEmpty { poster.withdraw(over.map { "permission-\(box)-\($0)" }) }
        for a in state.permissionWaits {
            guard let p = a.permission, last[a.id] != p else { continue }
            last[a.id] = p
            if !front, let n = NeedsYouNotice.permission(a, box: box, location: location) { poster.post(n) }
        }
        waits[box] = last
    }

    /// A session's tabs changed: a question that's new or different
    /// notifies (replacing the tab's last), and an answered one is taken
    /// back.
    public func tabs(box: String, tabs: [TabInfo], team: [TeamAgent], location: String?) {
        var last = questions[box] ?? [:]
        let front = inFront(box)
        var withdraw: [String] = []
        for (id, _) in last where !tabs.contains(where: { $0.id == id && $0.waiting != nil }) {
            last[id] = nil
            withdraw.append("waiting-\(box)-\(id)")
        }
        for t in tabs {
            guard let q = t.waiting, last[t.id] != q else { continue }
            last[t.id] = q
            if !front, let n = NeedsYouNotice.waiting(t, box: box, team: team, location: location) { poster.post(n) }
        }
        if !withdraw.isEmpty { poster.withdraw(withdraw.sorted()) }
        questions[box] = last
    }

    /// A session stopped following its workspace (its window closed or
    /// went back to the list): it leaves the badge and its notifications
    /// are taken back. The workspace keeps running (ADR 0023).
    public func closed(box: String) {
        let ids = (posted[box] ?? []).sorted() + (questions[box] ?? [:]).keys.map { "waiting-\(box)-\($0)" }.sorted()
            + (waits[box] ?? [:]).keys.map { "permission-\(box)-\($0)" }.sorted()
        if !ids.isEmpty { poster.withdraw(ids) }
        since[box] = nil
        seen[box] = nil
        posted[box] = nil
        questions[box] = nil
        waits[box] = nil
        counts[box] = nil
        updateBadge()
    }

    private func updateBadge() {
        let label = DockBadge.label(count)
        guard label != badge else { return }
        badge = label
        poster.setBadge(label)
    }
}
