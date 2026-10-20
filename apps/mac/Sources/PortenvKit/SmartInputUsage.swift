// SPDX-License-Identifier: Apache-2.0

import Foundation

/// One adapter's numbers (ADR 0027 S5a): numbers and the answering model's
/// name only, never a line, an answer or a key.
public struct AdapterCounts: Codable, Equatable, Sendable {
    public var requests = 0
    public var fallbacks = 0
    public var failures = 0
    public var inputTokens = 0
    public var outputTokens = 0
    public var model: String?
    /// The last `keep` calls' latencies, in milliseconds.
    public var latenciesMs: [Int] = []
    static let keep = 200

    public init() {}

    public var p50: Int? { percentile(0.50) }
    public var p95: Int? { percentile(0.95) }

    /// Nearest rank.
    private func percentile(_ p: Double) -> Int? {
        guard !latenciesMs.isEmpty else { return nil }
        let sorted = latenciesMs.sorted()
        let rank = Int((p * Double(sorted.count)).rounded(.up))
        return sorted[max(0, min(sorted.count - 1, rank - 1))]
    }
}

/// The local usage counter Settings › Experimental shows, per adapter:
/// requests, fallbacks, failures, p50 and p95 latency, and input and output
/// tokens. Kept on this Mac only (in the app's defaults when given), so
/// phase 2 can be compared against it.
public final class SmartInputUsage: @unchecked Sendable {
    public static let defaultsKey = "SmartInput.usage"
    /// The app's counter, in its defaults.
    public static let app = SmartInputUsage(defaults: .standard)
    private let lock = NSLock()
    private var counts: [String: AdapterCounts]
    private let defaults: UserDefaults?

    /// In memory when `defaults` is nil.
    public init(defaults: UserDefaults? = nil) {
        self.defaults = defaults
        counts = defaults?.data(forKey: Self.defaultsKey).flatMap { try? JSONDecoder().decode([String: AdapterCounts].self, from: $0) } ?? [:]
    }

    public func snapshot() -> [String: AdapterCounts] { lock.withLock { counts } }

    public func reset() {
        lock.withLock { counts = [:] }
        defaults?.removeObject(forKey: Self.defaultsKey)
    }

    /// A call that returned: its latency and tokens.
    func answered(_ adapter: String, ms: Int, usage: AdapterUsage) {
        change(adapter) {
            $0.requests += 1
            $0.inputTokens += usage.inputTokens
            $0.outputTokens += usage.outputTokens
            if let m = usage.model { $0.model = m }
            $0.latenciesMs.append(ms)
        }
    }

    /// A call that failed (an error, a timeout, garbage): the next layer
    /// was used.
    func failed(_ adapter: String, ms: Int) {
        change(adapter) {
            $0.requests += 1
            $0.failures += 1
            $0.fallbacks += 1
            $0.latenciesMs.append(ms)
        }
    }

    /// Skipped without a call (no key, the breaker open): the next layer
    /// was used.
    func skipped(_ adapter: String) { change(adapter) { $0.fallbacks += 1 } }

    /// An answer that was there but unusable as a suggestion (none, or
    /// below the threshold): the next layer was used.
    func fellBack(_ adapter: String) { change(adapter) { $0.fallbacks += 1 } }

    private func change(_ adapter: String, _ f: (inout AdapterCounts) -> Void) {
        let data: Data? = lock.withLock {
            var c = counts[adapter] ?? AdapterCounts()
            f(&c)
            if c.latenciesMs.count > AdapterCounts.keep { c.latenciesMs.removeFirst(c.latenciesMs.count - AdapterCounts.keep) }
            counts[adapter] = c
            return defaults == nil ? nil : try? JSONEncoder().encode(counts)
        }
        if let data { defaults?.set(data, forKey: Self.defaultsKey) }
    }
}
