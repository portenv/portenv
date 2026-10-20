// SPDX-License-Identifier: Apache-2.0

import Foundation

/// ADR 0027 §4a (R-0355): jev, TypeSafe's hosted "System One" model, as a
/// Classifier. It answers one typed question with a probability and writes
/// no text. What it sends: the typed line and the shell's name, the one
/// `intent` question and the model setting; nothing else (not the team,
/// the command list or the scripts). The key goes only in the
/// Authorization header.
public struct JevClassifier: Classifier {
    public static let endpoint = URL(string: "https://api.typesafe.ai/v1/systemone")!
    /// A setting; answers change between releases, so tests pin a version.
    public static let defaultModel = "jev-latest"
    /// A setting: 0.8 to start, until it's measured (ADR 0027 open question).
    public static let defaultThreshold = 0.8

    /// The one question's criteria, as §4a fixes them.
    static let criteria: [String: String] = [
        Intent.shell.rawValue: "a shell command to run as typed",
        Intent.english.rawValue: "a plain-language request for a command",
        Intent.portenv.rawValue: "a request about the Portenv team, agents, tabs or deploys",
    ]

    public let id = "jev"
    private let keys: any ProviderKeyStore
    private let model: String
    private let threshold: Double
    private let session: URLSession

    public init(keys: any ProviderKeyStore, model: String, threshold: Double, session: URLSession) {
        self.keys = keys
        self.model = model
        self.threshold = threshold
        self.session = session
    }

    public func classify(_ c: InputContext) async throws -> (Classification?, AdapterUsage) {
        let key = try await keys.required(.jev)
        var req = URLRequest(url: Self.endpoint)
        req.httpMethod = "POST"
        req.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try Self.body(c, model: model)
        return try Self.parse(try await SmartInputHTTP.send(req, on: session), threshold: threshold)
    }

    static func body(_ c: InputContext, model: String) throws -> Data {
        let body: [String: Any] = [
            "model": model,
            "state": ["line": c.line, "shell": c.shell.rawValue],
            "questions": ["intent": ["type": "choice", "criteria": criteria]],
        ]
        return try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])
    }

    /// `answers.intent.choice` when `answers.intent.confidence` is at least
    /// the threshold; below it, nil (unsure). A missing or malformed answer
    /// is `.malformed`.
    static func parse(_ data: Data, threshold: Double) throws -> (Classification?, AdapterUsage) {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let answer = (obj["answers"] as? [String: Any])?["intent"] as? [String: Any],
              let choice = (answer["choice"] as? String).flatMap(Intent.init(rawValue:)),
              let p = answer["confidence"] as? Double, (0 ... 1).contains(p)
        else { throw AdapterFailure.malformed }
        let u = obj["usage"] as? [String: Any]
        let usage = AdapterUsage(inputTokens: u?["input_tokens"] as? Int ?? 0, outputTokens: u?["output_tokens"] as? Int ?? 0,
                                 model: obj["model"] as? String)
        return (p >= threshold ? Classification(intent: choice, confidence: p) : nil, usage)
    }
}
