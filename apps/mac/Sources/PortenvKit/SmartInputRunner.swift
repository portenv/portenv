// SPDX-License-Identifier: Apache-2.0

import Foundation

/// ADR 0027 §4's budgets: a hosted classifier gets at most 150 ms after
/// Return, a suggester at most 2 s. Both wait on measured latency.
public struct SmartInputBudgets: Equatable, Sendable {
    public var classifier: Duration
    public var suggester: Duration
    public init(classifier: Duration = .milliseconds(150), suggester: Duration = .seconds(2)) {
        self.classifier = classifier
        self.suggester = suggester
    }
}

/// What Return does with the line.
public enum SmartInputOutcome: Equatable, Sendable {
    /// Send the line as typed: the bottom of every fall-through.
    case plainLine
    /// Replace the text with this one line, marked ✦; nothing runs until
    /// the next Return.
    case suggestion(String, intent: Intent)
}

/// The answer check (ADR 0027 §4): one line, at most 2 KB, no control
/// characters. Anything else is garbage.
public enum SuggestionCheck {
    public static let maxBytes = 2048

    public static func accept(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespaces)
        guard !t.isEmpty, t.utf8.count <= maxBytes,
              !t.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) || CharacterSet.newlines.contains($0) })
        else { return nil }
        return t
    }
}

/// One adapter's circuit breaker (ADR 0027 §4): three failures in a row
/// (an error, a timeout or garbage) skip it for 60 s; each further failure
/// after a cool-down doubles the skip, up to 15 min; a success resets it.
public struct CircuitBreaker: Equatable, Sendable {
    public static let threshold = 3
    public static let firstSkip: TimeInterval = 60
    public static let maxSkip: TimeInterval = 15 * 60

    private(set) var failures = 0
    private(set) var skip: TimeInterval = 0
    private(set) var openUntil: Date?

    public init() {}

    public func allows(at now: Date) -> Bool { openUntil.map { now >= $0 } ?? true }

    public mutating func succeeded() { self = CircuitBreaker() }

    public mutating func failed(at now: Date) {
        failures += 1
        guard failures >= Self.threshold else { return }
        skip = skip == 0 ? Self.firstSkip : min(skip * 2, Self.maxSkip)
        openUntil = now.addingTimeInterval(skip)
    }
}

/// The one fall-through runner (ADR 0027 §4, R-0354). It owns the order,
/// the budgets, the circuit breaker, the usage counter and the checks on
/// answers. Every error, timeout, missing key, missing adapter or malformed
/// answer skips that layer; the bottom is the plain line. It never shows a
/// dialog and never holds a keystroke: the caller's editor stays live.
public actor SmartInputRunner {
    private let enabled: @Sendable () -> Bool
    private let rules: any Classifier
    private let classifier: (any Classifier)?
    private let suggester: (any Suggester)?
    private let usage: SmartInputUsage
    private let budgets: SmartInputBudgets
    private let now: @Sendable () -> Date
    private var breakers: [String: CircuitBreaker] = [:]

    public init(enabled: @escaping @Sendable () -> Bool, rules: any Classifier = RuleClassifier(), classifier: (any Classifier)?,
                suggester: (any Suggester)?, usage: SmartInputUsage, budgets: SmartInputBudgets = SmartInputBudgets(),
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.enabled = enabled
        self.rules = rules
        self.classifier = classifier
        self.suggester = suggester
        self.usage = usage
        self.budgets = budgets
        self.now = now
    }

    /// The breaker's current skip, for tests and Settings.
    public func skipSeconds(_ adapter: String) -> TimeInterval { breakers[adapter]?.skip ?? 0 }

    public func decide(_ c: InputContext) async -> SmartInputOutcome {
        // Off is today's terminal: no rule, no call, no count.
        guard enabled() else { return .plainLine }
        let ruled = (try? await rules.classify(c))?.0
        let intent: Intent
        switch ruled?.intent {
        case .shell: return .plainLine
        case .english, .portenv: intent = ruled!.intent
        case nil:
            // Unsure: a hosted classifier, if any; unsure again is a command.
            guard let classifier, let hosted = await classify(c, with: classifier), hosted != .shell else { return .plainLine }
            intent = hosted
        }
        guard let suggester, let line = await suggest(c, intent: intent, with: suggester) else { return .plainLine }
        return .suggestion(line, intent: intent)
    }

    private func classify(_ c: InputContext, with a: any Classifier) async -> Intent? {
        let answer = await call(a.id, budget: budgets.classifier) { try await a.classify(c) }
        guard let (classification, _) = answer else { return nil }
        guard let classification else { usage.fellBack(a.id); return nil }
        return classification.intent
    }

    private func suggest(_ c: InputContext, intent: Intent, with a: any Suggester) async -> String? {
        let answer = await call(a.id, budget: budgets.suggester) { () async throws -> (String?, AdapterUsage) in
            let (s, u) = try await a.suggest(c, intent: intent)
            // Garbage is a failure for the breaker, as an error is.
            if let s, SuggestionCheck.accept(s) == nil { throw AdapterFailure.malformed }
            return (s, u)
        }
        guard let (text, _) = answer else { return nil }
        guard let text, let line = SuggestionCheck.accept(text) else { usage.fellBack(a.id); return nil }
        return line
    }

    /// One adapter call under its budget and its breaker; nil means fall
    /// through (and is counted).
    private func call<T: Sendable>(_ id: String, budget: Duration,
                                   _ op: @escaping @Sendable () async throws -> (T, AdapterUsage)) async -> (T, AdapterUsage)? {
        guard breakers[id, default: CircuitBreaker()].allows(at: now()) else {
            usage.skipped(id)
            return nil
        }
        let clock = ContinuousClock()
        let start = clock.now
        let result = await withBudget(budget, op)
        let elapsed = clock.now - start
        let ms = Int(elapsed.components.seconds * 1000 + elapsed.components.attoseconds / 1_000_000_000_000_000)
        switch result {
        case let .success(answer)?:
            breakers[id, default: CircuitBreaker()].succeeded()
            usage.answered(id, ms: ms, usage: answer.1)
            return answer
        case .failure(AdapterFailure.noKey)?:
            // No key: skipped, never a failure for the breaker.
            usage.skipped(id)
            return nil
        case .failure?, nil:
            breakers[id, default: CircuitBreaker()].failed(at: now())
            usage.failed(id, ms: ms)
            return nil
        }
    }
}

/// Runs `op`, giving up after `budget` (nil) without waiting for it: the
/// late call is cancelled and its answer, if any, dropped.
func withBudget<T: Sendable>(_ budget: Duration, _ op: @escaping @Sendable () async throws -> T) async -> Result<T, Error>? {
    let once = Once<Result<T, Error>?>()
    return await withCheckedContinuation { (k: CheckedContinuation<Result<T, Error>?, Never>) in
        once.set { k.resume(returning: $0) }
        let work = Task {
            do { once.resume(.success(try await op())) } catch { once.resume(.failure(error)) }
        }
        Task {
            try? await Task.sleep(for: budget)
            if !once.isDone {
                work.cancel()
                once.resume(nil)
            }
        }
    }
}
