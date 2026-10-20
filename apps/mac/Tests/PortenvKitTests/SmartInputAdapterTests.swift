// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// ADR 0027 S5a: the phase-1 adapters. Every adapter falls through on a
/// failure, a timeout and garbage; each sends only the fields R-0350 and
/// §4a allow; the key goes in one header and nowhere else.
struct SmartInputAdapterTests {
    static let anthropicKey = "fake-anthropic-key-not-real"
    static let jevKey = "fake-jev-key-not-real"

    let ctx = InputContext(line: "wake the writer", shell: .zsh, commandsVersion: "1.4.0",
                           team: [TeamAgentRef(id: "writer", name: "Writer", role: "Code Writer")], scripts: nil)
    let commands = [PortenvCommandSummary(path: ["wake"], summary: "Wake an agent.", args: ["agent"])]

    func json(_ d: Data) throws -> NSDictionary { try #require(JSONSerialization.jsonObject(with: d) as? NSDictionary) }

    static let anthropicOK = """
    {"id":"msg_1","type":"message","model":"claude-opus-5-5","stop_reason":"end_turn",
     "content":[{"type":"thinking","thinking":""},{"type":"text","text":"portenv wake writer"}],
     "usage":{"input_tokens":312,"output_tokens":9}}
    """

    static let jevOK = """
    {"model":"jev-2026-10-01","answers":{"intent":{"choice":"portenv","confidence":0.93}},
     "usage":{"input_tokens":41,"output_tokens":3}}
    """

    func anthropic(_ net: StubNetwork, keys: FakeKeys = FakeKeys([.anthropic: anthropicKey])) -> AnthropicSuggester {
        AnthropicSuggester(keys: keys, model: "claude-opus-5-5", commands: commands, session: net.session)
    }

    func jev(_ net: StubNetwork, keys: FakeKeys = FakeKeys([.jev: jevKey]), threshold: Double = 0.8) -> JevClassifier {
        JevClassifier(keys: keys, model: "jev-2026-10-01", threshold: threshold, session: net.session)
    }

    // MARK: Anthropic

    /// Exactly what leaves the Mac for a suggestion (R-0350): the sentence,
    /// the shell, the command list, the team's IDs, names and roles; the key
    /// only in x-api-key.
    @Test func anthropicSendsOnlyTheListedFields() async throws {
        let net = StubNetwork(.json(200, Self.anthropicOK))
        let (answer, usage) = try await anthropic(net).suggest(ctx, intent: .portenv)
        #expect(answer == "portenv wake writer")
        #expect(usage.inputTokens == 312 && usage.outputTokens == 9 && usage.model == "claude-opus-5-5")

        let seen = try #require(net.seen.first)
        #expect(net.seen.count == 1)
        #expect(seen.url.absoluteString == "https://api.anthropic.com/v1/messages")
        #expect(seen.method == "POST")
        #expect(seen.headers["x-api-key"] == Self.anthropicKey)
        #expect(seen.headers["anthropic-version"] == "2023-06-01")
        #expect(Set(seen.headers.keys) == ["x-api-key", "anthropic-version", "Content-Type", "Content-Length"])

        let body = try json(seen.body)
        #expect(Set(body.allKeys as! [String]) == ["model", "max_tokens", "output_config", "system", "messages"])
        #expect(body["model"] as? String == "claude-opus-5-5")
        #expect(body["system"] as? String == AnthropicSuggester.instructions)
        let messages = try #require(body["messages"] as? [[String: Any]])
        #expect(messages.count == 1 && messages[0]["role"] as? String == "user")
        let content = try #require(messages[0]["content"] as? String)
        let payload = try json(Data(content.utf8))
        let expected: NSDictionary = [
            "line": "wake the writer", "shell": "zsh", "intent": "portenv",
            "commands": [["command": "portenv wake", "args": ["agent"], "summary": "Wake an agent."]],
            "team": [["id": "writer", "name": "Writer", "role": "Code Writer"]],
        ]
        #expect(payload == expected)
        #expect(!String(decoding: seen.body, as: UTF8.self).contains(Self.anthropicKey))
    }

    /// Script names go only with the opt-in (the context has them).
    @Test func anthropicSendsScriptsOnlyWhenTheContextHasThem() async throws {
        let net = StubNetwork(.json(200, Self.anthropicOK))
        let withScripts = InputContext(line: "run the tests", shell: .bash, commandsVersion: "1.4.0", team: [], scripts: ["test", "build"])
        _ = try await anthropic(net).suggest(withScripts, intent: .english)
        let body = try json(try #require(net.seen.first).body)
        let content = try #require((body["messages"] as? [[String: Any]])?.first?["content"] as? String)
        #expect(try json(Data(content.utf8))["scripts"] as? [String] == ["test", "build"])
    }

    @Test func anthropicWithNoKeyMakesNoCall() async throws {
        let net = StubNetwork(.json(200, Self.anthropicOK))
        await #expect(throws: AdapterFailure.noKey) { try await anthropic(net, keys: FakeKeys()).suggest(ctx, intent: .english) }
        #expect(net.seen.isEmpty)
    }

    /// A refused key is a plain failure whose text never holds the key.
    @Test func anthropicHTTPErrorIsAFailureWithoutTheKey() async throws {
        let net = StubNetwork(.json(401, #"{"type":"error","error":{"message":"invalid x-api-key fake-anthropic-key-not-real"}}"#))
        do {
            _ = try await anthropic(net).suggest(ctx, intent: .english)
            Issue.record("expected a failure")
        } catch {
            #expect(error as? AdapterFailure == .http(401))
            #expect(!"\(error)".contains(Self.anthropicKey))
            #expect(!error.localizedDescription.contains(Self.anthropicKey))
        }
    }

    @Test(arguments: [
        "not json",
        #"{"content":[]}"#,
        #"{"content":[{"type":"text","text":"ls\nrm -rf /"}],"usage":{"input_tokens":1,"output_tokens":1}}"#,
        #"{"content":[{"type":"text","text":"echo \u001b]52;c;aGk=\u0007"}]}"#,
        #"{"content":[{"type":"text","text":"   "}]}"#,
    ])
    func anthropicGarbageIsMalformed(_ reply: String) async throws {
        let net = StubNetwork(.json(200, reply))
        await #expect(throws: AdapterFailure.malformed) { try await anthropic(net).suggest(ctx, intent: .english) }
    }

    @Test func anthropicTooLongAnswerIsMalformed() async throws {
        let long = String(repeating: "a", count: 2049)
        let net = StubNetwork(.json(200, #"{"content":[{"type":"text","text":"echo \#(long)"}]}"#))
        await #expect(throws: AdapterFailure.malformed) { try await anthropic(net).suggest(ctx, intent: .english) }
    }

    /// "No command" and a refusal are no answer, not a failure.
    @Test func anthropicNoneAndRefusalAreNoAnswer() async throws {
        for reply in [#"{"content":[{"type":"text","text":"NONE"}],"usage":{"input_tokens":5,"output_tokens":1}}"#,
                      #"{"stop_reason":"refusal","content":[],"usage":{"input_tokens":5,"output_tokens":0}}"#] {
            let (answer, usage) = try await anthropic(StubNetwork(.json(200, reply))).suggest(ctx, intent: .english)
            #expect(answer == nil)
            #expect(usage.inputTokens == 5)
        }
    }

    @Test func anthropicStripsOneSurroundingBacktickPair() async throws {
        let net = StubNetwork(.json(200, #"{"content":[{"type":"text","text":"`git status`"}]}"#))
        #expect(try await anthropic(net).suggest(ctx, intent: .english).0 == "git status")
    }

    // MARK: jev (§4a)

    /// Exactly what leaves the Mac for a classification: the typed line and
    /// the shell's name, the one intent question, the pinned model.
    @Test func jevSendsOnlyTheLineAndTheShell() async throws {
        let net = StubNetwork(.json(200, Self.jevOK))
        let (c, usage) = try await jev(net).classify(ctx)
        #expect(c?.intent == .portenv && c?.confidence == 0.93)
        #expect(usage.inputTokens == 41 && usage.outputTokens == 3 && usage.model == "jev-2026-10-01")

        let seen = try #require(net.seen.first)
        #expect(seen.url.absoluteString == "https://api.typesafe.ai/v1/systemone")
        #expect(seen.method == "POST")
        #expect(seen.headers["Authorization"] == "Bearer \(Self.jevKey)")
        #expect(Set(seen.headers.keys) == ["Authorization", "Content-Type", "Content-Length"])
        let expected: NSDictionary = [
            "model": "jev-2026-10-01",
            "state": ["line": "wake the writer", "shell": "zsh"],
            "questions": ["intent": ["type": "choice", "criteria": [
                "shell": "a shell command to run as typed",
                "english": "a plain-language request for a command",
                "portenv": "a request about the Portenv team, agents, tabs or deploys",
            ]]],
        ]
        #expect(try json(seen.body) == expected)
        let text = String(decoding: seen.body, as: UTF8.self)
        for absent in ["Writer", "Code Writer", "1.4.0", Self.jevKey] { #expect(!text.contains(absent)) }
    }

    @Test func jevBelowTheThresholdIsUnsure() async throws {
        let net = StubNetwork(.json(200, #"{"model":"jev-2026-10-01","answers":{"intent":{"choice":"english","confidence":0.79}},"usage":{"input_tokens":4,"output_tokens":1}}"#))
        let (c, usage) = try await jev(net).classify(ctx)
        #expect(c == nil)
        #expect(usage.inputTokens == 4)
    }

    @Test func jevWithNoKeyMakesNoCall() async throws {
        let net = StubNetwork(.json(200, Self.jevOK))
        await #expect(throws: AdapterFailure.noKey) { try await jev(net, keys: FakeKeys()).classify(ctx) }
        #expect(net.seen.isEmpty)
    }

    @Test(arguments: [
        "<html>",
        #"{"answers":{}}"#,
        #"{"answers":{"intent":{"choice":"delete","confidence":0.99}}}"#,
        #"{"answers":{"intent":{"choice":"shell","confidence":"high"}}}"#,
        #"{"answers":{"intent":{"choice":"shell","confidence":1.7}}}"#,
    ])
    func jevGarbageIsMalformed(_ reply: String) async throws {
        await #expect(throws: AdapterFailure.malformed) { try await jev(StubNetwork(.json(200, reply))).classify(ctx) }
    }

    @Test func jevHTTPAndTransportErrorsAreFailures() async throws {
        await #expect(throws: AdapterFailure.http(503)) { try await jev(StubNetwork(.json(503, "{}"))).classify(ctx) }
        await #expect(throws: AdapterFailure.transport) { try await jev(StubNetwork(.fail(.timedOut))).classify(ctx) }
    }

    /// Answers change between releases: the model is a setting, jev-latest
    /// by default, and tests pin one.
    @Test func jevModelIsASettingDefaultingToLatest() {
        let d = UserDefaults(suiteName: "SmartInputAdapterTests-\(UUID())")!
        #expect(SmartInputSettings(defaults: d).jevModel == "jev-latest")
        #expect(SmartInputSettings(defaults: d).jevThreshold == 0.8)
        #expect(SmartInputSettings(defaults: d).anthropicModel == AnthropicSuggester.defaultModel)
    }

    // MARK: the built-in rules

    @Test(arguments: ["git status", "ls -la", "./build.sh", "/usr/bin/env", "~/bin/x", "FOO=1 make", "echo hi | wc -l",
                      "a && b", "cat > f", "make", "cd ..", "npm test"])
    func rulesCallShellLinesShell(_ line: String) async throws {
        let c = try await RuleClassifier().classify(InputContext(line: line, shell: .bash, commandsVersion: "", team: [], scripts: nil))
        #expect(c.0?.intent == .shell)
    }

    @Test(arguments: ["wake the writer", "show me the biggest files here", "what is using port 3000?", "deploy to staging please"])
    func rulesCallStrongSentencesEnglish(_ line: String) async throws {
        let c = try await RuleClassifier().classify(InputContext(line: line, shell: .bash, commandsVersion: "", team: [], scripts: nil))
        #expect(c.0?.intent == .english)
    }

    @Test(arguments: ["foo", "fix it", "hello there friend"])
    func rulesAreUnsureOtherwise(_ line: String) async throws {
        let c = try await RuleClassifier().classify(InputContext(line: line, shell: .bash, commandsVersion: "", team: [], scripts: nil))
        #expect(c.0 == nil)
    }

    // MARK: context minimisation (R-0350)

    @Test func theContextIsRedactedAndCapped() {
        let c = InputContext.minimal(line: "deploy with password=hunter2 and sk-ant-api03-AAAABBBBCCCCDDDDEEEEFFFF", shell: .zsh,
                                     commandsVersion: "1.4.0", team: [], scripts: (0 ..< 150).map { "s\($0)" })
        #expect(c?.line == "deploy with <secret> and <secret>")
        #expect(c?.scripts?.count == 100)
        #expect(InputContext.minimal(line: String(repeating: "x", count: 501), shell: .zsh, commandsVersion: "", team: [], scripts: nil) == nil)
        let ghp = InputContext.minimal(line: "use ghp_abcdefghijklmnopqrstuvwxyz0123456789", shell: .bash, commandsVersion: "", team: [], scripts: nil)
        #expect(ghp?.line == "use <secret>")
    }
}
