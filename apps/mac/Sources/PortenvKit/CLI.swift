// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Runs the portenv CLI, which talks to portenvd (`portenv app …`).
public protocol CLIRunning: Sendable {
    /// Runs `portenv` with arguments and returns its standard output, or
    /// throws with its standard error.
    func run(_ arguments: [String]) async throws -> String
}

/// A failed portenv command, with what it said.
public struct CLIError: Error, LocalizedError, Equatable {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Where the Portenv binaries are: PORTENV_BIN_DIR, inside Portenv.app
/// (make app), or the repository's bin/ when the app runs from Xcode (make
/// build puts them there).
public enum Binaries {
    public static var directory: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_BIN_DIR"], !dir.isEmpty {
            return URL(fileURLWithPath: dir)
        }
        // Portenv.app (make app) carries its own portenv, portenvd and restic.
        let inside = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers")
        if Bundle.main.bundleURL.pathExtension == "app",
           FileManager.default.isExecutableFile(atPath: inside.appendingPathComponent("portenv").path) {
            return inside
        }
        // apps/mac/Sources/PortenvKit/CLI.swift → the repository root.
        return URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("bin")
    }
    public static var portenv: URL { directory.appendingPathComponent("portenv") }
    public static var portenvd: URL { directory.appendingPathComponent("portenvd") }

    /// The Portenv directory portenvd serves from (PORTENV_HOME, or
    /// Application Support).
    public static var home: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_HOME"], !dir.isEmpty {
            return URL(fileURLWithPath: dir)
        }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Portenv")
    }
    public static var socket: URL { home.appendingPathComponent("portenvd.sock") }
}

/// Runs the real portenv binary.
public struct CLI: CLIRunning {
    public let executable: URL
    public init(executable: URL = Binaries.portenv) { self.executable = executable }

    public func run(_ arguments: [String]) async throws -> String {
        let executable = self.executable
        return try await withCheckedThrowingContinuation { continuation in
            let process = Process()
            process.executableURL = executable
            process.arguments = arguments
            let out = Pipe(), err = Pipe()
            process.standardOutput = out
            process.standardError = err
            process.standardInput = FileHandle.nullDevice
            process.terminationHandler = { p in
                let stdout = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                let stderr = String(decoding: err.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                if p.terminationStatus == 0 {
                    continuation.resume(returning: stdout.trimmingCharacters(in: .whitespacesAndNewlines))
                } else {
                    let message = stderr.split(separator: "\n").last.map(String.init) ?? "portenv exited with \(p.terminationStatus)"
                    continuation.resume(throwing: CLIError(message.replacingOccurrences(of: "portenv: ", with: "")))
                }
            }
            do { try process.run() } catch { continuation.resume(throwing: error) }
        }
    }
}
