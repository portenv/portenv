// SPDX-License-Identifier: Apache-2.0

import GRPCCore
import Testing

@testable import PortenvKit

/// What portenvd's transport says is never shown: no socket path (it holds
/// the person's user name), no gRPC text (R-0202). portenvd's own plain
/// lines pass through.
struct DaemonErrorTextTests {
    @Test(arguments: [
        (RPCError.Code.unavailable, "Could not establish a connection to [unix]/Users/someone/Library/Application Support/Portenv/portenvd.sock"),
        (.unavailable, "Could not establish a connection to unix:/x/portenvd.sock"),
        (.unavailable, "Channel is in a transient failure state"),
        (.unavailable, "Channel isn't ready."),
        (.unavailable, "The RPC was cancelled because the channel is shutting down."),
        (.unknown, "connection reset by peer: /Users/someone/Library/Application Support/Portenv-dev-C/portenvd.sock"),
        (.cancelled, "RPC was cancelled by the client."),
    ] as [(RPCError.Code, String)])
    func transportTextIsNeverShown(code: RPCError.Code, message: String) {
        let line = DaemonErrorText.plain(code: code, message: message)
        #expect(line == DaemonErrorText.starting)
        for raw in ["/Users", "unix", ".sock", "Channel", "RPC", "gRPC", "someone"] {
            #expect(!line.contains(raw))
        }
        // As DaemonClient makes it: transport text means portenvd isn't there.
        let e = DaemonError.fromRPC(message: line, unimplemented: false, unavailable: code == .unavailable || DaemonErrorText.isTransport(message))
        #expect(DaemonErrorText.isServiceDown(e))
    }

    @Test func portenvdsOwnLinesPassThrough() {
        for m in ["There's no workspace named acme-api here.", "Confirm test-server to open this.", "A workspace named acme-api already exists."] {
            #expect(DaemonErrorText.plain(code: .failedPrecondition, message: m) == m)
        }
        #expect(DaemonErrorText.starting == "Portenv\u{2019}s background service isn\u{2019}t running. Portenv is starting it\u{2026}")
    }
}
