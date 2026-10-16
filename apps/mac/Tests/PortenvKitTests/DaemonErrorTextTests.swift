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

/// The engine's own failures, as portenvd passes them on, become plain
/// lines (R-0211): never Docker's text, a socket path or an address.
struct EngineErrorTextTests {
    @Test(arguments: [
        ("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", DaemonErrorText.engineNotRunning),
        ("create container: Cannot connect to the Docker daemon at unix:///Users/someone/.docker/run/docker.sock. Is the docker daemon running?", DaemonErrorText.engineNotRunning),
        ("inspect image: Internal Server Error: dialing 192.168.65.7:2376: context deadline exceeded", DaemonErrorText.engineStarting),
        ("Error response from daemon: Docker Desktop is starting", DaemonErrorText.engineStarting),
        ("pull ghcr.io/portenv/toolbox-node:main: write /var/lib/docker/tmp/x: no space left on device", DaemonErrorText.noDisk),
        ("start: failed to create shim task: no space left on device", DaemonErrorText.noDisk),
        ("pull ghcr.io/portenv/toolbox-node:main: Error response from daemon: manifest unknown", DaemonErrorText.noImage),
        ("Error response from daemon: No such image: ghcr.io/portenv/toolbox-node:main", DaemonErrorText.noImage),
        ("pull ghcr.io/portenv/toolbox-node:main: dial tcp: lookup ghcr.io: no such host", DaemonErrorText.noImage),
    ])
    func engineTextIsAPlainLine(message: String, want: String) {
        let line = DaemonErrorText.plain(code: .unknown, message: message)
        #expect(line == want)
        for raw in ["unix", "/var", "/Users", "192.168", "2376", "daemon", "ghcr.io", "manifest", "someone"] {
            #expect(!line.contains(raw))
        }
        // An engine that's down isn't Portenv's own service being down.
        #expect(!DaemonErrorText.isServiceDown(DaemonError(line, unavailable: DaemonErrorText.isTransport(message))))
    }
}
