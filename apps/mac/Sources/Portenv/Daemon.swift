// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit

/// Starts portenvd when it is not already running and stops the one it
/// started when the app quits. (A login item from 1.1.)
actor Daemon {
    static let shared = Daemon()
    private var process: Process?

    func ensureRunning() async {
        if FileManager.default.fileExists(atPath: Binaries.socket.path), await answers() { return }
        let p = Process()
        p.executableURL = Binaries.portenvd
        p.standardInput = FileHandle.nullDevice
        do { try p.run() } catch { return }
        process = p
        for _ in 0..<50 where !FileManager.default.fileExists(atPath: Binaries.socket.path) {
            try? await Task.sleep(for: .milliseconds(100))
        }
    }

    /// Stops the portenvd this app started (SIGTERM: it saves and releases
    /// open boxes first), waiting up to two minutes.
    func stop() async {
        guard let p = process, p.isRunning else { return }
        p.terminate()
        for _ in 0..<1200 where p.isRunning {
            try? await Task.sleep(for: .milliseconds(100))
        }
    }

    private func answers() async -> Bool {
        (try? await CLI().run(["app", "ping"])) != nil
    }
}
