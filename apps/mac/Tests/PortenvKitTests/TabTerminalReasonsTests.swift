// SPDX-License-Identifier: Apache-2.0

import GRPCCore
import Testing

@testable import PortenvKit

/// The Disconnected card says what happened in a plain line, never a raw
/// gRPC status (ADR 0020; GUIDELINES.md §10).
struct TabTerminalReasonsTests {
    typealias R = TabTerminalReasons

    @Test(arguments: [
        // grpc-swift's own, when portenvd's socket isn't there.
        (RPCError.Code.unavailable, "Could not establish a connection to unix:/x/portenvd.sock", R.serviceDown, false),
        (.unavailable, "the workspace helper is unavailable (its channel is down or failed verification); restart the workspace", R.helperDown, false),
        (.unavailable, "tab terminal: the tmux control client isn't running", R.helperDown, false),
        (.unavailable, "12 bytes of input weren't delivered: can't find pane: %3", R.typingLost, false),
        (.failedPrecondition, "This workspace's tmux is too old for the app's terminal (needs 3.4 or later).", R.tmuxTooOld, true),
        (.failedPrecondition, "acme-api is not open on this Mac", R.closed, true),
        (.failedPrecondition, "the workspace helper didn't say which workspace it runs in, so nothing was sent to it; restart the workspace", R.wrongHelper, true),
        (.failedPrecondition, "the connection reached another workspace's helper, so nothing was sent to it; restart the workspace", R.wrongHelper, true),
        (.failedPrecondition, "tab terminals: no control-mode client here", R.noTabTerminals, true),
        (.failedPrecondition, "acme-api: open box.json: no such file", R.closed, true),
        (.notFound, "no such box", R.closed, true),
        (.permissionDenied, "this caller may reach only its own workspace", R.notAllowed, true),
        (.unauthenticated, "portenvd couldn't tell who is calling", R.notAllowed, true),
        (.deadlineExceeded, "the tab's state didn't come", R.tooSlow, false),
        (.invalidArgument, "the first tab terminal message must be open", R.unreadable, true),
        (.unimplemented, "unknown method portenv.daemon.v1.DaemonService/TabTerminal", DaemonError.outOfDate, true),
        (.internalError, "seed: display: \"%1 80\"", R.lost, false),
        (.cancelled, "", R.lost, false),
        (.unknown, "EOF", R.lost, false),
    ] as [(RPCError.Code, String, String, Bool)])
    func eachRefusalIsAPlainLine(code: RPCError.Code, message: String, want: String, permanent: Bool) {
        let e = TabTerminalStreamError(rpc: RPCError(code: code, message: message))
        #expect(e.message == want)
        #expect(e.permanent == permanent)
        // Never the raw status, its code, or the message as portenvd wrote it.
        #expect(!e.message.contains(message) || message.isEmpty)
        #expect(!e.message.contains("\(code)"))
        #expect(e.message.first?.isUppercase == true && e.message.hasSuffix("."))
    }
}
