// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Tells portenvd the system's network status (from NWPathMonitor), so a box
/// opens offline at once when there is no network at all instead of waiting
/// for a storage probe. portenvd trusts a "down" only from a running app and
/// for 30 s after it was sent, so while there is no network this reporter
/// sends it again at least every `downRefresh`; "up" is sent on changes only.
public actor NetworkReporter {
    public static let downRefresh: Duration = .seconds(10)

    private let daemon: DaemonAPI
    private let now: @Sendable () -> ContinuousClock.Instant
    private var last: Bool?
    private var sentAt: ContinuousClock.Instant?

    public init(daemon: DaemonAPI, now: @escaping @Sendable () -> ContinuousClock.Instant = { .now }) {
        self.daemon = daemon
        self.now = now
    }

    /// Reports whether some network is usable. Returns true once portenvd has
    /// a current report.
    @discardableResult
    public func report(usable: Bool) async -> Bool {
        if last == usable, let at = sentAt, usable || now() - at < Self.downRefresh {
            return true
        }
        do {
            try await daemon.setNetwork(usable: usable)
            last = usable
            sentAt = now()
            return true
        } catch {
            return false // portenvd probes without a report; the next call retries
        }
    }

    /// Sends "down" again when it is due (call it every few seconds).
    public func refresh() async {
        if last == false { await report(usable: false) }
    }

    /// Forgets what was sent (portenvd restarted): the next report is sent.
    public func reset() {
        last = nil
        sentAt = nil
    }
}
