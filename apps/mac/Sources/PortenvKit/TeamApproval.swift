// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation

// Approving an agent file (ADR 0022, R-0285): the owner sees the file's
// full text, its SHA-256 and its grants, and Approve pins exactly that
// hash in portenvd. Only the owner's app approves: an approval takes a
// review the app fetched from portenvd itself, never anything an agent sent.

/// An agent file as portenvd has it (GetAgentFile).
public struct AgentFileContent: Equatable, Sendable {
    /// The file as it is now.
    public var content: String
    /// portenvd's hash of it.
    public var sha256: String
    /// The text last approved; empty if it never was.
    public var approvedContent: String
    public var runner: String

    public init(content: String, sha256: String, approvedContent: String, runner: String) {
        self.content = content
        self.sha256 = sha256
        self.approvedContent = approvedContent
        self.runner = runner
    }
}

/// A string's SHA-256, as lowercase hex.
public enum SHA256Text {
    public static func of(_ s: String) -> String { AgentFileReview.sha256(s) }
}

/// One line of what changed since the last approval.
public struct AgentFileLine: Equatable, Sendable {
    public enum Kind: Equatable, Sendable { case same, removed, added }
    public var kind: Kind
    public var text: String
}

/// What Approve shows and pins: the whole file, the hash of exactly that
/// text, its runner, and what changed since it was last approved.
public struct AgentFileReview: Equatable, Sendable {
    public let agentID: String
    public let content: String
    /// The SHA-256 of `content`, worked out here: Approve sends this, so
    /// the pin is the text the owner saw.
    public let sha256: String
    public let runner: String
    /// Whether some earlier version was approved.
    public let approvedBefore: Bool
    /// Approved before, and the file isn't that version now.
    public let changed: Bool
    /// Every line, marked against the approved version; all `same` when
    /// there's nothing to compare with.
    public let diff: [AgentFileLine]

    /// Nil when the text doesn't hash to what portenvd says it has: then
    /// what would be shown isn't what would be pinned.
    public init?(agentID: String, file: AgentFileContent) {
        let sha = Self.sha256(file.content)
        guard sha == file.sha256.lowercased() else { return nil }
        self.agentID = agentID
        content = file.content
        sha256 = sha
        runner = TeamText.line(file.runner, max: 300)
        approvedBefore = !file.approvedContent.isEmpty
        changed = approvedBefore && file.approvedContent != file.content
        diff = changed ? Self.diff(from: file.approvedContent, to: file.content) : Self.lines(file.content).map { .init(kind: .same, text: $0) }
    }

    static func sha256(_ s: String) -> String {
        SHA256.hash(data: Data(s.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    static func lines(_ s: String) -> [String] {
        var out = s.components(separatedBy: "\n")
        if out.last == "" { out.removeLast() }
        return out
    }

    /// The lines of `new`, with the lines of `old` it dropped, in order.
    static func diff(from old: String, to new: String) -> [AgentFileLine] {
        let a = lines(old), b = lines(new)
        var removed = Set<Int>(), inserted = Set<Int>()
        for change in b.difference(from: a) {
            switch change {
            case .remove(let offset, _, _): removed.insert(offset)
            case .insert(let offset, _, _): inserted.insert(offset)
            }
        }
        var out: [AgentFileLine] = []
        var i = 0, j = 0
        while i < a.count || j < b.count {
            if i < a.count, removed.contains(i) {
                out.append(.init(kind: .removed, text: a[i])); i += 1
            } else if j < b.count, inserted.contains(j) {
                out.append(.init(kind: .added, text: b[j])); j += 1
            } else {
                if j < b.count { out.append(.init(kind: .same, text: b[j])) }
                i += 1; j += 1
            }
        }
        return out
    }
}

public extension TeamText {
    static let approveFile = "Approve"
    static let start = "Start"
    static let stop = "Stop"
    /// Under Start while the team's first start waits: the card is above
    /// it in the agent's column, or in Needs you for an agent it doesn't
    /// name.
    static let approveTeamStartAbove = "Approve the team\u{2019}s first start above."
    static let approveTeamStartInNeedsYou = "Approve the team\u{2019}s first start in Needs you."
    /// portenvd's refusal of a Start while the team's first start waits
    /// (core/daemon/team.go), written for the CLI, which shows it as it is
    /// (core/cmd/portenv/owner.go). The app shows its hint instead.
    static let teamStartWaitingRefusal = "approve the team's first start in Portenv first"
    static let fileNotShownExactly = "Portenv couldn\u{2019}t show this file exactly as it is, so it can\u{2019}t be approved. Try again."

    static func approveFirst(_ name: String) -> String { "Approve \(name)\u{2019}s file first." }

    /// "Approve Lead's file", the review's heading.
    static func approveHeading(_ name: String, changed: Bool) -> String {
        changed ? "\(name)\u{2019}s file changed. Approve it again?" : "Approve \(name)\u{2019}s file?"
    }

    /// Where the file stands, in words.
    static func approval(_ a: TeamAgent, review: AgentFileReview?) -> String {
        if a.approved { return "Approved" }
        return review?.approvedBefore == true ? "Changed since you approved it" : "Not approved yet"
    }

    /// "SHA-256 3f2a…", the hash in full, for the owner to compare.
    static func hashLine(_ sha: String) -> String { "SHA-256 \(sha)" }

    /// The grants, one line each, as the owner approves them.
    static func grants(_ g: TeamGrant) -> [String] {
        let panes = switch g.panes {
        case .never: "Panes: none"
        case .ask: "Panes: asks you, up to \(g.maxPanes)"
        case .allow: "Panes: opens up to \(g.maxPanes) without asking"
        }
        let others = g.message.map { TeamText.line($0, max: 64) }.filter { !$0.isEmpty }
        let messages: String = switch others.count {
        case 0: "Messages: only you"
        case 1: "Messages: you and \(others[0])"
        default: "Messages: you, \(others.dropLast().joined(separator: ", ")) and \(others.last!)"
        }
        let packages = g.packages == .never ? "Packages: never" : "Packages: asks you"
        return [panes, messages, g.askOwner ? "Asks you questions" : "Doesn\u{2019}t ask you questions", packages]
    }

    /// An answered decision's line, in place of Approve and Decline
    /// (R-0296): when it was answered and, for the team's first start, the
    /// agents still starting. Nil while it's pending.
    static func answered(_ d: TeamDecision, team: [TeamAgent]) -> String? {
        let at = d.answeredAt.map { " at " + $0.formatted(date: .omitted, time: .shortened) } ?? ""
        switch d.state {
        case .pending: return nil
        case .expired: return "Expired"
        case .declined: return "Declined" + at
        case .approved:
            if let chosen = d.chosen { return "Chose \u{201C}\(chosen)\u{201D}" + at }
            let starting = d.kind == .teamStart
                ? d.startAgents.compactMap { id in team.first { $0.id == id } }.filter { [.stopped, .starting].contains($0.status) }.map(\.name)
                : []
            guard !starting.isEmpty else { return "Approved" + at }
            return "Approved" + at + " \u{00B7} starting " + ListFormatter.localizedString(byJoining: starting) + "\u{2026}"
        }
    }

    /// Who asks a decision: the agent's name, or Portenv for its own.
    static func asker(_ d: TeamDecision, team: [TeamAgent]) -> String {
        if let a = team.first(where: { $0.id == d.from }) { return a.name }
        return d.from == "portenv" ? "Portenv" : TeamText.line(d.from, max: 64)
    }

    /// The file's text with any control character shown as its symbol, so
    /// nothing in it is hidden from the owner (the hash is of the file).
    static func visible(_ s: String) -> String {
        var out = String.UnicodeScalarView()
        for u in s.unicodeScalars {
            switch u.value {
            case 0x0A, 0x09: out.append(u)
            case 0x00 ... 0x1F: out.append(Unicode.Scalar(0x2400 + u.value)!)
            case 0x7F: out.append("\u{2421}")
            case 0x80 ... 0x9F, 0x200E, 0x200F, 0x202A ... 0x202E, 0x2066 ... 0x2069:
                out.append(contentsOf: String(format: "<U+%04X>", u.value).unicodeScalars)
            default: out.append(u)
            }
        }
        return String(out)
    }
}
