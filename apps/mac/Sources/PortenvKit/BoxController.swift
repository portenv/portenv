// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// One box's window: where the box is, what the title subtitle says, and the
/// title-menu actions. Every action goes through portenvd.
@MainActor
@Observable
public final class BoxController {
    /// Where the box is open.
    public enum Location: Equatable, Sendable {
        case closed
        case thisMac
        case server(String)
    }

    public let box: String
    public private(set) var location: Location = .closed
    public private(set) var subtitle = "Opening…"
    public private(set) var servers: [String] = []
    public private(set) var busy = false
    /// The last failure, shown in a sheet; cleared by dismissError.
    public private(set) var error: String?
    /// Changes whenever the box starts on this Mac again, so the terminal
    /// reattaches to the new start's tmux session.
    public private(set) var terminalGeneration = 0
    /// The box agent stopped answering: nothing can be saved, and unsaved
    /// changes stay in the box until Restart Box.
    public private(set) var agentUnavailable = false

    private let cli: CLIRunning

    public init(box: String, cli: CLIRunning) {
        self.box = box
        self.cli = cli
    }

    /// Opens the box on this Mac.
    public func open() async {
        await perform("Opening…") {
            self.servers = (try? await self.cli.run(["app", "servers", self.box]))?
                .split(separator: "\n").map(String.init) ?? []
            let summary = try await self.cli.run(["app", "open", self.box])
            self.location = .thisMac
            self.terminalGeneration += 1
            return summary.contains("Offline") ? "Offline · will save later" : "Saved"
        }
    }

    /// File › Make Save Point (⌘S).
    public func makeSavePoint() async {
        guard location == .thisMac else { return }
        await perform("Saving…") {
            _ = try await self.cli.run(["app", "point", self.box])
            return "Save point made " + Self.time()
        }
    }

    /// Revert To ▸ Last Save Point. The home as it was is saved first.
    public func revertToLastSavePoint() async {
        guard location == .thisMac else { return }
        await perform("Reverting…") {
            _ = try await self.cli.run(["app", "revert", self.box])
            return "Reverted to the last save point " + Self.time()
        }
    }

    /// Move To ▸ This Mac or a server.
    public func move(to target: String) async {
        let label = target == "this-mac" ? "this Mac" : target
        await perform("Moving to \(label)…") {
            _ = try await self.cli.run(["app", "move", self.box, target])
            if target == "this-mac" {
                self.location = .thisMac
                self.terminalGeneration += 1
                return "Moved to this Mac " + Self.time()
            }
            self.location = .server(target)
            return "Open on \(target)"
        }
    }

    /// Closing the window saves and releases the box.
    public func close() async {
        guard location == .thisMac else { return }
        await perform("Saving…") {
            _ = try await self.cli.run(["app", "close", self.box])
            self.location = .closed
            return "Closed"
        }
    }

    public func dismissError() { error = nil }

    /// The terminal's connection ended without the user leaving it: ask
    /// portenvd whether the box agent still answers; reattach if it does.
    public func terminalEnded() async {
        guard location == .thisMac, !busy else { return }
        do {
            _ = try await cli.run(["app", "check", box])
            terminalGeneration += 1
        } catch {
            if Self.isAgentUnavailable(error) { agentUnavailable = true }
        }
    }

    /// Restart Box: stops the box and opens it again; the local home and
    /// its unsaved changes stay (portenvd's resume rules never restore over
    /// them).
    public func restartBox() async {
        guard location == .thisMac, !busy else { return }
        busy = true
        defer { busy = false }
        do {
            _ = try await cli.run(["app", "restart", box])
            agentUnavailable = false
            terminalGeneration += 1
        } catch {
            self.error = error.localizedDescription
        }
    }

    static func isAgentUnavailable(_ error: Error) -> Bool {
        error.localizedDescription.contains("the box agent is unavailable")
    }

    private func perform(_ progress: String, _ action: @escaping @MainActor () async throws -> String) async {
        guard !busy else { return }
        busy = true
        let before = subtitle
        subtitle = progress
        defer { busy = false }
        do {
            subtitle = try await action()
        } catch {
            subtitle = before
            self.error = error.localizedDescription
            if Self.isAgentUnavailable(error) { agentUnavailable = true }
        }
    }

    private static func time() -> String {
        let f = DateFormatter()
        f.timeStyle = .short
        return "at " + f.string(from: Date())
    }
}
