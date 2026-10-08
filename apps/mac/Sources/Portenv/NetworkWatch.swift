// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import PortenvKit

/// Watches the system's network status (NWPathMonitor) and reports it to
/// portenvd, refreshing a "down" so it never goes stale while the app runs.
final class NetworkWatch: @unchecked Sendable {
    static let shared = NetworkWatch()

    let reporter = NetworkReporter()
    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "com.portenv.network")
    private var started = false

    /// Starts watching and returns once the first status has been reported
    /// (or after half a second; without a report portenvd probes storage).
    func start() async {
        guard !started else { return }
        started = true
        let first = AsyncStream<Void>(bufferingPolicy: .bufferingNewest(1)) { continuation in
            monitor.pathUpdateHandler = { [reporter] path in
                let usable = path.status == .satisfied
                Task {
                    await reporter.report(usable: usable)
                    continuation.yield()
                }
            }
        }
        monitor.start(queue: queue)
        Task { [reporter] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                await reporter.refresh()
            }
        }
        await withTaskGroup(of: Void.self) { group in
            group.addTask { for await _ in first { return } }
            group.addTask { try? await Task.sleep(for: .milliseconds(500)) }
            await group.next()
            group.cancelAll()
        }
    }
}
