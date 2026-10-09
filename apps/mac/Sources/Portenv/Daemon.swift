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
        // Its log goes to ~/Library/Logs/Portenv/portenvd.log: started from
        // Finder or `open`, this app's own output goes nowhere.
        if let log = Self.logFile() {
            p.standardOutput = log
            p.standardError = log
        }
        // If this app ends without stopping it (killed, crashed), portenvd
        // still saves, releases and exits.
        var env = ProcessInfo.processInfo.environment
        env["PORTENVD_EXIT_WITH_PARENT"] = "1"
        p.environment = env
        do { try p.run() } catch { return }
        process = p
        // Until it answers, not until the socket file exists: after a crash
        // the old one is still there.
        for _ in 0..<100 {
            if await answers() { break }
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

    /// The daemon's log file, appended to (created 0600 in a 0700 folder).
    static func logFile() -> FileHandle? {
        let fm = FileManager.default
        let dir = fm.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/Portenv")
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let url = dir.appendingPathComponent("portenvd.log")
        if !fm.fileExists(atPath: url.path) {
            fm.createFile(atPath: url.path, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        guard let h = try? FileHandle(forWritingTo: url) else { return nil }
        h.seekToEndOfFile()
        return h
    }

    private func answers() async -> Bool {
        (try? await CLI().run(["app", "ping"])) != nil
    }
}
