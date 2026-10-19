// SPDX-License-Identifier: Apache-2.0

import Foundation

// The owner sees every message that involves their agents (ADR 0016 point 5,
// R-0313): an agent's Team section (every other message it sent or
// received) and Team activity (the whole board), with Portenv's notices
// greyed. Board text reaches here already plain (TeamText.plain); these are
// the words around it.

extension TeamState {
    /// An agent's Team section: every message it sent or received that
    /// isn't in its conversation with the owner, and Portenv's notices about
    /// it, in board order (the order they were written, oldest first).
    public func teamMessages(of id: String) -> [TeamMessage] {
        let withYou = Set(conversation(with: id).map(\.id))
        return messages.filter { m in
            !withYou.contains(m.id) && (m.from == id || m.to == id || (m.from == TeamText.portenv && m.about == id))
        }
    }

    /// Team activity: every message on the board in board order, or, for
    /// one agent, what it sent, what it received and Portenv's notices
    /// about it.
    public func activity(filter agent: String? = nil) -> [TeamMessage] {
        guard let agent else { return messages }
        return messages.filter { $0.from == agent || $0.to == agent || $0.about == agent }
    }
}

/// The words for board messages in the agent's column and Team activity.
public enum TeamMessageText {
    public static let messagesWithYou = "Messages with you"
    public static let activity = "Team activity"
    public static let everyone = "Everyone"
    public static let showAll = "Show all"
    public static let hint = "Return to send \u{00B7} Shift-Return for a new line"
    public static let send = "Send"
    public static let noTeamMessages = "No messages with the team yet."
    public static let noActivity = "No messages yet."
    /// A message is at most 8 KB (ADR 0022 §7), counted in UTF-8 bytes as
    /// portenvd counts it.
    public static let maxBytes = 8 * 1024
    /// An opened entry shows this much, then Show all.
    public static let previewLines = 12
    public static let previewCharacters = 600

    /// "Team · 4".
    public static func teamHeading(_ n: Int) -> String { "Team \u{00B7} \(n)" }

    /// The field's placeholder and VoiceOver name: "Message Builder".
    public static func placeholder(_ agent: String) -> String { "Message \(agent)" }

    /// Whether Portenv itself wrote it: shown greyed.
    public static func isNotice(_ m: TeamMessage) -> Bool { m.from == TeamText.portenv }

    /// Who a board id is: "You", "Portenv", or the agent's name (its id
    /// when the team doesn't list it).
    public static func name(_ id: String, team: [TeamAgent]) -> String {
        switch id {
        case TeamText.owner: return TeamText.you
        case TeamText.portenv: return "Portenv"
        default:
            let name = team.first { $0.id == id }?.name ?? ""
            return name.isEmpty ? TeamText.line(id, max: 64) : name
        }
    }

    /// "Lead → Builder".
    public static func route(_ m: TeamMessage, team: [TeamAgent]) -> String {
        "\(name(m.from, team: team)) \u{2192} \(name(m.to, team: team))"
    }

    /// "14:28", as the state line writes times; "" without one.
    public static func time(_ at: Date?, timeZone: TimeZone = .current) -> String {
        guard let at else { return "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "HH:mm"
        return f.string(from: at)
    }

    /// The first line with words on it, as one line.
    public static func firstLine(_ text: String) -> String {
        let line = text.split(separator: "\n").first { !$0.allSatisfy(\.isWhitespace) } ?? ""
        return TeamText.line(String(line), max: 200)
    }

    /// The entry as VoiceOver reads it: "Lead to Builder, 14:28: Take the
    /// retry half"; "Notice from Portenv to Builder, …" for Portenv's.
    public static func spoken(_ m: TeamMessage, team: [TeamAgent], timeZone: TimeZone = .current) -> String {
        let who = "\(name(m.from, team: team)) to \(name(m.to, team: team))"
        let at = time(m.createdAt, timeZone: timeZone)
        let head = (isNotice(m) ? "Notice from " : "") + (at.isEmpty ? who : "\(who), \(at)")
        return "\(head): \(firstLine(m.text))"
    }

    /// What an opened entry shows before Show all, or nil when the whole
    /// text fits: its first 12 lines or 600 characters, whichever is less.
    public static func preview(_ text: String) -> String? {
        var cut = text.startIndex
        var lines = 0
        var count = 0
        while cut < text.endIndex {
            if count == previewCharacters { break }
            if text[cut] == "\n" {
                lines += 1
                if lines == previewLines { break }
            }
            cut = text.index(after: cut)
            count += 1
        }
        guard cut < text.endIndex else { return nil }
        return String(text[..<cut]) + "\u{2026}"
    }

    /// Why the field won't send this, as one line that says what to do;
    /// nil when it can go.
    public static func refusal(_ text: String) -> String? {
        let bytes = text.utf8.count
        guard bytes > maxBytes else { return nil }
        let kb = (Double(bytes) / 1024 * 10).rounded(.up) / 10
        let shown = kb.truncatingRemainder(dividingBy: 1) == 0 ? String(Int(kb)) : String(format: "%.1f", kb)
        return "A message can be at most 8 KB; this one is \(shown) KB. Shorten it and send again."
    }
}
