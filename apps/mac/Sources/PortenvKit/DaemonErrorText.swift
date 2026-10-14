// SPDX-License-Identifier: Apache-2.0

import Foundation
import GRPCCore

/// What the app says about portenvd's own failures (R-0202; GUIDELINES.md
/// §10): never gRPC's transport text or the socket's path, which holds the
/// person's user name. portenvd's own plain lines pass through. Shared:
/// first run and the box window use it, and the tab terminal's Disconnected
/// lines (TabTerminalReasons) build on it.
public enum DaemonErrorText {
    public static let starting = "Portenv\u{2019}s background service isn\u{2019}t running. Portenv is starting it\u{2026}"

    /// grpc-swift's own words, and anything naming the socket or a path.
    static func isTransport(_ message: String) -> Bool {
        let m = message.lowercased()
        return ["unix:", "[unix]", ".sock", "/users/", "/private/", "/var/", "channel is", "channel isn't", "could not establish a connection",
                "transient failure", "rpc was cancelled", "connection reset", "connection refused", "transport"].contains { m.contains($0) }
    }

    /// The line to show for a status: portenvd's own, or, for the
    /// transport's, that the service is starting.
    public static func plain(code _: RPCError.Code, message: String) -> String {
        isTransport(message) ? starting : message
    }

    /// portenvd isn't there right now: worth waiting for, not a refusal.
    public static func isServiceDown(_ error: Error) -> Bool {
        if let e = error as? DaemonError { return e.unavailable && (e.message == starting || isTransport(e.message)) }
        if let e = error as? CreateBoxError { return e.kind == .daemonGone || e.message == starting }
        return false
    }
}
