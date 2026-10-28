// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What portenvd's FetchAgentBranch brought in: refs/remotes/agents/<id>
/// in the owner's project, at that commit.
public struct FetchedBranch: Equatable, Sendable {
    public var ref: String
    public var commit: String

    public init(ref: String, commit: String) {
        self.ref = ref
        self.commit = commit
    }
}

/// The line under an agent's column after the owner's Fetch: what came
/// in, or portenvd's refusal, plain.
public struct FetchLine: Equatable, Sendable {
    public var text: String
    public var failed: Bool

    public init(text: String, failed: Bool) {
        self.text = text
        self.failed = failed
    }
}

/// The owner's Fetch (ADR 0022 §2a): an agent's branch comes back to the
/// owner only this way, never as a patch or a file to apply.
public enum TeamFetch {
    public static let button = "Fetch"

    /// The branch an agent works on in its own copy.
    public static func branch(_ id: String) -> String { "agent/\(id)" }

    /// "Fetch agent/writer", in the agent's column controls.
    public static func menuTitle(_ id: String) -> String { "Fetch \(branch(id))" }

    /// Whether Fetch shows on this message: portenv share's, from an agent
    /// with its own copy, naming its own branch. A message that only reads
    /// like one doesn't carry the branch, so it gets none.
    public static func shows(on m: TeamMessage, for agent: TeamAgent) -> Bool {
        agent.ownClone && m.from == agent.id && m.sharedBranch == branch(agent.id)
    }

    /// Whether the agent's column offers Fetch: it has its own copy and has
    /// shared its branch.
    public static func canFetch(_ agent: TeamAgent, messages: [TeamMessage]) -> Bool {
        messages.contains { shows(on: $0, for: agent) }
    }

    /// "Fetched agent/writer into your project as agents/writer
    /// (0123456789ab). Review it with git, then merge."
    public static func fetched(id: String, commit: String) -> String {
        "Fetched \(branch(id)) into your project as agents/\(id) (\(commit.prefix(12))). Review it with git, then merge."
    }
}
