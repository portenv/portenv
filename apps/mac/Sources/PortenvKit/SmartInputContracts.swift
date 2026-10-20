// SPDX-License-Identifier: Apache-2.0

import Foundation

// ADR 0027 §7: the Classifier and Suggester contracts (R-0354). App code
// talks only to these protocols; each vendor is one adapter file. One
// fall-through runner (SmartInputRunner) owns the order, the budgets, the
// circuit breaker, the usage counter and the checks on answers; adapters
// own none of these. Additive conformances only (Equatable, inits).

public enum ShellKind: String, Sendable { case bash, zsh, fish, unknown }

/// A team agent as the suggester may see it (R-0350): ID, name, role.
public struct TeamAgentRef: Equatable, Sendable {
    public let id: String
    public let name: String
    public let role: String
    public init(id: String, name: String, role: String) {
        self.id = id
        self.name = name
        self.role = role
    }
}

/// Built once per Return, already redacted (R-0350). It has no field for
/// file contents, messages, output, history or the environment, so no
/// adapter can send them.
public struct InputContext: Equatable, Sendable {
    public let line: String
    public let shell: ShellKind
    public let commandsVersion: String
    public let team: [TeamAgentRef]
    /// Present only with the opt-in (script names, at most 100).
    public let scripts: [String]?

    public init(line: String, shell: ShellKind, commandsVersion: String, team: [TeamAgentRef], scripts: [String]?) {
        self.line = line
        self.shell = shell
        self.commandsVersion = commandsVersion
        self.team = team
        self.scripts = scripts
    }
}

public enum Intent: String, Sendable { case shell, english, portenv }

public struct Classification: Equatable, Sendable {
    public let intent: Intent
    public let confidence: Double
    public init(intent: Intent, confidence: Double) {
        self.intent = intent
        self.confidence = confidence
    }
}

public struct AdapterUsage: Equatable, Sendable {
    public var inputTokens = 0, outputTokens = 0
    public var model: String?
    public init(inputTokens: Int = 0, outputTokens: Int = 0, model: String? = nil) {
        self.inputTokens = inputTokens
        self.outputTokens = outputTokens
        self.model = model
    }
}

public protocol Classifier: Sendable { // built-in rules (default), jev, …
    var id: String { get }
    func classify(_ c: InputContext) async throws -> (Classification?, AdapterUsage) // nil = unsure
}

public protocol Suggester: Sendable { // a hosted LLM, …
    var id: String { get }
    func suggest(_ c: InputContext, intent: Intent) async throws -> (String?, AdapterUsage)
}

/// One call that does both (phase 2's Portenv cloud adapter, R-0356).
public protocol ClassifyingSuggester: Classifier, Suggester {
    func classifyAndSuggest(_ c: InputContext) async throws -> (Classification?, String?, AdapterUsage)
}

/// Why an adapter gave up. Each case falls through to the next layer
/// (R-0354); none ever carries a key, a line or a response body.
public enum AdapterFailure: Error, Equatable, Sendable, CustomStringConvertible {
    /// No key in the Keychain (or it couldn't be read): the adapter is
    /// skipped. Not a failure for the circuit breaker.
    case noKey
    /// The service answered with this HTTP status.
    case http(Int)
    /// The answer wasn't one the adapter can use (garbage).
    case malformed
    /// The request didn't complete (offline, refused, cancelled).
    case transport

    public var description: String {
        switch self {
        case .noKey: "no key"
        case let .http(s): "HTTP \(s)"
        case .malformed: "malformed answer"
        case .transport: "no answer"
        }
    }
}

/// One `portenv` command as the suggester sees it (ADR 0027 §7's command
/// list, reduced to what R-0350 lets it send).
public struct PortenvCommandSummary: Equatable, Sendable {
    public let path: [String]
    public let summary: String
    public let args: [String]
    public init(path: [String], summary: String, args: [String]) {
        self.path = path
        self.summary = summary
        self.args = args
    }
}

/// R-0350's limits, applied before any adapter sees the context.
public extension InputContext {
    static let maxLine = 500
    static let maxScripts = 100

    /// A redacted context, or nil when the sentence is too long to send.
    static func minimal(line: String, shell: ShellKind, commandsVersion: String, team: [TeamAgentRef], scripts: [String]?) -> InputContext? {
        guard line.count <= maxLine else { return nil }
        let r = SecretRedaction.redact
        return InputContext(line: r(line), shell: shell, commandsVersion: commandsVersion,
                            team: team.map { TeamAgentRef(id: r($0.id), name: r($0.name), role: r($0.role)) },
                            scripts: scripts.map { Array($0.prefix(maxScripts)).map(r) })
    }
}

/// ADR 0017's secret words plus token shapes; each match becomes
/// `<secret>` (R-0350).
public enum SecretRedaction {
    public static let marker = "<secret>"

    // A secret word with whatever is assigned to it ("password=hunter2",
    // "token: abc"), or the bare word.
    nonisolated(unsafe) private static let word = /(?i)\b(pass(word|phrase)|\bpin\b|verification code|one-time code|\b2fa\b|\botp\b|token|api[ _-]?key|secret|private key|credentials?|access key)(\s*[:=]\s*\S+)?/
    // Token shapes: well-known prefixes, AWS key IDs, JWTs, long hex or
    // base64 runs.
    nonisolated(unsafe) private static let shapes = /\b(sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9\-]{10,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+|[A-Fa-f0-9]{32,}|[A-Za-z0-9+\/_\-]{40,}={0,2})/

    public static func redact(_ s: String) -> String {
        // Words first: the marker itself holds a secret word.
        s.replacing(word) { _ in marker }.replacing(shapes) { _ in marker }
    }
}
