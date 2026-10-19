// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A classifier or suggester that does what the test says, and counts.
final class ScriptedAdapter: Classifier, Suggester, @unchecked Sendable {
    enum Act: Sendable { case answer(Intent, Double), suggest(String?), fail, hang, garbage, noKey }
    let id: String
    private let lock = NSLock()
    private var acts: [Act]
    private var _calls = 0
    var calls: Int { lock.withLock { _calls } }
    private var _hangsFinished = 0
    /// Hangs that ran their whole 30 s instead of being cancelled.
    var hangsFinished: Int { lock.withLock { _hangsFinished } }
    init(_ id: String, _ acts: Act...) { self.id = id; self.acts = acts }

    private func next() -> Act {
        lock.withLock {
            _calls += 1
            return acts.count > 1 ? acts.removeFirst() : acts[0]
        }
    }

    private func run<T>(_ ok: (Act) -> T?) async throws -> T? {
        let act = next()
        switch act {
        case .fail: throw AdapterFailure.http(500)
        case .garbage: throw AdapterFailure.malformed
        case .noKey: throw AdapterFailure.noKey
        case .hang:
            try await Task.sleep(for: .seconds(30))
            lock.withLock { _hangsFinished += 1 }
            return nil
        default: return ok(act)
        }
    }

    func classify(_: InputContext) async throws -> (Classification?, AdapterUsage) {
        let c: Classification? = try await run { if case let .answer(i, p) = $0 { Classification(intent: i, confidence: p) } else { nil } }
        return (c, AdapterUsage(inputTokens: 10, outputTokens: 1, model: "m"))
    }

    func suggest(_: InputContext, intent _: Intent) async throws -> (String?, AdapterUsage) {
        let s: String? = try await run { if case let .suggest(s) = $0 { s } else { nil } }
        return (s, AdapterUsage(inputTokens: 100, outputTokens: 7, model: "m"))
    }
}

/// A clock the test moves.
final class BreakerClock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = Date(timeIntervalSince1970: 1_000_000)
    var now: Date { lock.withLock { t } }
    func advance(_ s: TimeInterval) { lock.withLock { t += s } }
}

/// ADR 0027 §4: every layer falls through to the plain line; the breaker;
/// the usage counter; the toggle off means no calls at all.
struct SmartInputRunnerTests {
    // Roomier than the ADR's, so a busy test machine never times out an answer.
    let fast = SmartInputBudgets(classifier: .seconds(1), suggester: .seconds(1))

    func context(_ line: String) -> InputContext {
        InputContext(line: line, shell: .zsh, commandsVersion: "1.4.0", team: [], scripts: nil)
    }

    func runner(on: Bool = true, classifier: (any Classifier)? = nil, suggester: (any Suggester)? = nil,
                usage: SmartInputUsage = SmartInputUsage(), clock: BreakerClock = BreakerClock()) -> SmartInputRunner {
        SmartInputRunner(enabled: { on }, rules: RuleClassifier(), classifier: classifier, suggester: suggester,
                         usage: usage, budgets: fast, now: { clock.now })
    }

    @Test func theTimeoutsAreTheADRs() {
        #expect(SmartInputBudgets().classifier == .milliseconds(150))
        #expect(SmartInputBudgets().suggester == .seconds(2))
    }

    /// Off is today's terminal: nothing is asked, nothing is counted.
    @Test func offMakesNoCalls() async {
        let c = ScriptedAdapter("jev", .answer(.english, 1))
        let s = ScriptedAdapter("anthropic", .suggest("ls"))
        let usage = SmartInputUsage()
        let r = runner(on: false, classifier: c, suggester: s, usage: usage)
        for line in ["wake the writer", "fix it", "git status"] { #expect(await r.decide(context(line)) == .plainLine) }
        #expect(c.calls == 0 && s.calls == 0)
        #expect(usage.snapshot().isEmpty)
    }

    @Test func theToggleIsOffByDefault() {
        let d = UserDefaults(suiteName: "SmartInputRunnerTests-\(UUID())")!
        #expect(SmartInputSettings(defaults: d).enabled == false)
    }

    /// A line the rules call shell never reaches a hosted adapter.
    @Test func aShellLineNeverReachesAHostedAdapter() async {
        let c = ScriptedAdapter("jev", .answer(.english, 1))
        let s = ScriptedAdapter("anthropic", .suggest("ls"))
        let r = runner(classifier: c, suggester: s)
        for line in ["git status", "ls | wc -l", "./run.sh"] { #expect(await r.decide(context(line)) == .plainLine) }
        #expect(c.calls == 0 && s.calls == 0)
    }

    @Test func englishGoesStraightToTheSuggester() async {
        let c = ScriptedAdapter("jev", .answer(.shell, 1))
        let s = ScriptedAdapter("anthropic", .suggest("portenv wake writer"))
        let r = runner(classifier: c, suggester: s)
        #expect(await r.decide(context("wake the writer")) == .suggestion("portenv wake writer", intent: .english))
        #expect(c.calls == 0 && s.calls == 1)
    }

    @Test func anUnsureLineAsksTheClassifierThenTheSuggester() async {
        let c = ScriptedAdapter("jev", .answer(.portenv, 0.9))
        let s = ScriptedAdapter("anthropic", .suggest("portenv team start"))
        #expect(await runner(classifier: c, suggester: s).decide(context("fix it")) == .suggestion("portenv team start", intent: .portenv))
    }

    @Test func aClassifierThatSaysShellSendsThePlainLine() async {
        let c = ScriptedAdapter("jev", .answer(.shell, 0.95))
        let s = ScriptedAdapter("anthropic", .suggest("x"))
        #expect(await runner(classifier: c, suggester: s).decide(context("fix it")) == .plainLine)
        #expect(s.calls == 0)
    }

    /// The classifier's failure, timeout, garbage, missing key: the rules
    /// decide, and unsure is a command.
    @Test(arguments: [ScriptedAdapter.Act.fail, .hang, .garbage, .noKey])
    func aFailedClassifierFallsThroughToThePlainLine(_ act: ScriptedAdapter.Act) async {
        let c = ScriptedAdapter("jev", act)
        let s = ScriptedAdapter("anthropic", .suggest("x"))
        let usage = SmartInputUsage()
        #expect(await runner(classifier: c, suggester: s, usage: usage).decide(context("fix it")) == .plainLine)
        // The runner never waited a hang out: its budget cancelled it (no
        // wall-clock reading; the whole suite shares the cooperative pool).
        #expect(c.hangsFinished == 0)
        #expect(s.calls == 0)
        #expect(usage.snapshot()["jev"]?.fallbacks == 1)
    }

    /// The suggester's failure, timeout, garbage, missing key: the line.
    @Test(arguments: [ScriptedAdapter.Act.fail, .hang, .garbage, .noKey, .suggest(nil), .suggest("ls\nrm -rf ~")])
    func aFailedSuggesterFallsThroughToThePlainLine(_ act: ScriptedAdapter.Act) async {
        let s = ScriptedAdapter("anthropic", act)
        let usage = SmartInputUsage()
        #expect(await runner(suggester: s, usage: usage).decide(context("wake the writer")) == .plainLine)
        #expect(s.hangsFinished == 0)
        #expect(usage.snapshot()["anthropic"]?.fallbacks == 1)
    }

    @Test func noAdaptersMeansThePlainLine() async {
        #expect(await runner().decide(context("wake the writer")) == .plainLine)
        #expect(await runner().decide(context("fix it")) == .plainLine)
    }

    /// Three failures in a row skip the adapter for 60 s; each failure after
    /// a cool-down doubles the skip, up to 15 min; a success resets it.
    @Test func theBreakerOpensCoolsDownAndDoubles() async {
        let clock = BreakerClock()
        let s = ScriptedAdapter("anthropic", .fail)
        let r = runner(suggester: s, clock: clock)
        let line = context("wake the writer")
        for _ in 0 ..< 3 { _ = await r.decide(line) }
        #expect(s.calls == 3)
        _ = await r.decide(line)
        #expect(s.calls == 3, "open: skipped")
        clock.advance(59)
        _ = await r.decide(line)
        #expect(s.calls == 3)
        clock.advance(2)
        _ = await r.decide(line)
        #expect(s.calls == 4, "cooled down: one try")
        clock.advance(119)
        _ = await r.decide(line)
        #expect(s.calls == 4, "failed again: 120 s")
        var skip: TimeInterval = 120
        for _ in 0 ..< 6 {
            clock.advance(skip + 1)
            _ = await r.decide(line)
            skip = min(skip * 2, 900)
        }
        #expect(await r.skipSeconds("anthropic") == 900)
    }

    @Test func aSuccessResetsTheBreaker() async {
        let clock = BreakerClock()
        let s = ScriptedAdapter("anthropic", .fail, .fail, .suggest("ls"), .fail, .fail, .suggest("ls"))
        let r = runner(suggester: s, clock: clock)
        for _ in 0 ..< 6 { _ = await r.decide(context("wake the writer")) }
        #expect(s.calls == 6, "never three in a row: never open")
    }

    /// The breaker trips on timeouts too, so a dead service stops adding its
    /// budget to every line.
    @Test func theBreakerCountsTimeouts() async {
        let s = ScriptedAdapter("anthropic", .hang)
        let r = runner(suggester: s)
        for _ in 0 ..< 3 { _ = await r.decide(context("wake the writer")) }
        #expect(await r.decide(context("wake the writer")) == .plainLine)
        #expect(s.calls == 3, "open: not called, so no budget spent")
    }

    /// A missing key is a skip, not a failure: it never opens the breaker.
    @Test func aMissingKeyNeverOpensTheBreaker() async {
        let s = ScriptedAdapter("anthropic", .noKey)
        let r = runner(suggester: s)
        for _ in 0 ..< 5 { _ = await r.decide(context("wake the writer")) }
        #expect(s.calls == 5)
    }

    // MARK: the usage counter

    @Test func theCounterRecordsRequestsFallbacksFailuresLatencyAndTokens() async {
        let usage = SmartInputUsage()
        let c = ScriptedAdapter("jev", .answer(.english, 0.9), .fail)
        let s = ScriptedAdapter("anthropic", .suggest("ls -S"), .garbage)
        let r = runner(classifier: c, suggester: s, usage: usage)
        _ = await r.decide(context("fix it"))
        _ = await r.decide(context("fix it"))
        _ = await r.decide(context("wake the writer"))
        let snap = usage.snapshot()
        let jev = try! #require(snap["jev"])
        #expect(jev.requests == 2 && jev.failures == 1 && jev.fallbacks == 1)
        #expect(jev.inputTokens == 10 && jev.outputTokens == 1 && jev.model == "m")
        let a = try! #require(snap["anthropic"])
        #expect(a.requests == 2 && a.failures == 1 && a.fallbacks == 1)
        #expect(a.inputTokens == 100 && a.outputTokens == 7)
        #expect(a.latenciesMs.count == 2)
        #expect(a.p50 != nil && a.p95 != nil)
    }

    @Test func percentiles() {
        var c = AdapterCounts()
        for ms in 1 ... 100 { c.latenciesMs.append(ms) }
        #expect(c.p50 == 50 && c.p95 == 95)
        #expect(AdapterCounts().p50 == nil)
    }

    /// The counter keeps numbers only, and survives a relaunch.
    @Test func theCounterPersistsNumbersOnly() async throws {
        let d = UserDefaults(suiteName: "SmartInputRunnerTests-\(UUID())")!
        let usage = SmartInputUsage(defaults: d)
        let s = ScriptedAdapter("anthropic", .suggest("portenv wake writer"))
        _ = await runner(suggester: s, usage: usage).decide(context("wake the writer"))
        let again = SmartInputUsage(defaults: d)
        #expect(again.snapshot()["anthropic"]?.requests == 1)
        let stored = String(decoding: try #require(d.data(forKey: SmartInputUsage.defaultsKey)), as: UTF8.self)
        for absent in ["wake", "writer", "portenv"] { #expect(!stored.contains(absent)) }
        again.reset()
        #expect(SmartInputUsage(defaults: d).snapshot().isEmpty)
    }

    /// End to end with the real adapters on stubbed network: the key from
    /// the fake store reaches only its own host's header.
    @Test func realAdaptersEndToEnd() async throws {
        let net = StubNetwork { seen in
            seen.url.host == "api.typesafe.ai"
                ? .json(200, #"{"model":"jev-1","answers":{"intent":{"choice":"english","confidence":0.9}},"usage":{"input_tokens":3,"output_tokens":1}}"#)
                : .json(200, #"{"content":[{"type":"text","text":"du -sh * | sort -h"}],"usage":{"input_tokens":50,"output_tokens":6}}"#)
        }
        let keys = FakeKeys([.anthropic: "A-KEY-123456", .jev: "J-KEY-654321"])
        let r = runner(classifier: JevClassifier(keys: keys, model: "jev-1", threshold: 0.8, session: net.session),
                       suggester: AnthropicSuggester(keys: keys, model: "claude-opus-5-5", commands: [], session: net.session))
        #expect(await r.decide(context("fix it")) == .suggestion("du -sh * | sort -h", intent: .english))
        #expect(net.seen.count == 2)
        let jev = try #require(net.seen.first { $0.url.host == "api.typesafe.ai" })
        let ant = try #require(net.seen.first { $0.url.host == "api.anthropic.com" })
        #expect(jev.headers["Authorization"] == "Bearer J-KEY-654321" && jev.headers["x-api-key"] == nil)
        #expect(ant.headers["x-api-key"] == "A-KEY-123456" && ant.headers["Authorization"] == nil)
    }

    @Test func toggleOffWithRealAdaptersMakesNoNetworkCall() async {
        let net = StubNetwork(.json(200, "{}"))
        let keys = FakeKeys([.anthropic: "A", .jev: "J"])
        let r = runner(on: false, classifier: JevClassifier(keys: keys, model: "jev-1", threshold: 0.8, session: net.session),
                       suggester: AnthropicSuggester(keys: keys, model: "m", commands: [], session: net.session))
        for line in ["fix it", "wake the writer"] { #expect(await r.decide(context(line)) == .plainLine) }
        #expect(net.seen.isEmpty)
        #expect(keys.reads == 0, "off: no Keychain read either")
    }
}
