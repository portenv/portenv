// SPDX-License-Identifier: Apache-2.0

import Foundation
import GRPCCore

/// What the app says about portenvd's own failures and the engine's (R-0202,
/// R-0211; GUIDELINES.md
/// §10): never gRPC's transport text or the socket's path, which holds the
/// person's user name. portenvd's own plain lines pass through. Shared:
/// first run and the box window use it, and the tab terminal's Disconnected
/// lines (TabTerminalReasons) build on it.
public enum DaemonErrorText {
    public static let starting = "Portenv\u{2019}s background service isn\u{2019}t running. Portenv is starting it\u{2026}"

    // The engine's failures, as portenvd passes them on (R-0211). Docker
    // Desktop is what runs workspaces on a Mac, so the lines name it.
    public static let engineNotRunning = "Portenv uses Docker Desktop to run workspaces on this Mac. Start Docker Desktop, then click Try Again."
    public static let engineStarting = "Docker Desktop is still starting. Wait a moment, then click Try Again."
    public static let noDisk = "This Mac is out of disk space for workspaces. Free some space, then click Try Again."
    public static let noImage = "Portenv couldn\u{2019}t get the workspace image. Check this Mac is online, then click Try Again."

    /// The engine's line for a message, or nil when it isn't the engine's.
    static func engineLine(_ message: String) -> String? {
        let m = message.lowercased()
        if m.contains("no space left on device") { return noDisk }
        if m.contains("docker desktop is starting") || (m.contains("dialing") && m.contains("deadline exceeded")) { return engineStarting }
        if m.contains("cannot connect to the docker daemon") || m.contains("is the docker daemon running") { return engineNotRunning }
        if m.contains("no such image") || m.contains("manifest unknown") || m.contains("pull access denied") || (m.contains("pull ") && m.contains("no such host")) {
            return noImage
        }
        return nil
    }

    /// grpc-swift's own words, and anything naming the socket or a path.
    /// The engine's failures aren't: they name Docker's socket, not ours.
    static func isTransport(_ message: String) -> Bool {
        if engineLine(message) != nil { return false }
        let m = message.lowercased()
        return ["unix:", "[unix]", ".sock", "/users/", "/private/", "/var/", "channel is", "channel isn't", "could not establish a connection",
                "transient failure", "rpc was cancelled", "connection reset", "connection refused", "transport"].contains { m.contains($0) }
    }

    /// The line to show for a status: portenvd's own, or, for the
    /// transport's, that the service is starting.
    public static func plain(code _: RPCError.Code, message: String) -> String {
        if let line = engineLine(message) { return line }
        return isTransport(message) ? starting : message
    }

    /// portenvd isn't there right now: worth waiting for, not a refusal.
    public static func isServiceDown(_ error: Error) -> Bool {
        if let e = error as? DaemonError { return e.unavailable && (e.message == starting || isTransport(e.message)) }
        if let e = error as? CreateBoxError { return e.kind == .daemonGone || e.message == starting }
        return false
    }
}
