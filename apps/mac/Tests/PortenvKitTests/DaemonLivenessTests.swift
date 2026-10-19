// SPDX-License-Identifier: Apache-2.0

import Darwin
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import PortenvProto
import SwiftProtobuf
import Testing

@testable import PortenvKit

/// A frozen portenvd (R-0294, the owner's step 7b: SIGSTOP for 35 s) turns
/// into "Reconnecting…" through the real DaemonClient: a fake portenvd
/// that answers TabTerminal with a reset and then stays quiet, behind a
/// proxy that stops passing bytes either way, as a stopped process does
/// (the kernel still accepts the connection; nothing answers on it).
@MainActor
struct DaemonLivenessTests {
    static let liveness = DaemonClient.Liveness(time: .seconds(1), timeout: .seconds(1), backoffMax: .milliseconds(300))
    static let retry = ControlModeTabSource.Retry(first: .milliseconds(50), longest: .milliseconds(200), attempts: 1000,
                                                  firstReset: .seconds(2), settle: .milliseconds(200))
    let size = TabTerminalSize(cols: 80, rows: 24)

    final class Events {
        var all: [TabTerminalEvent] = []
        var resets: Int { all.filter { if case .reset = $0 { true } else { false } }.count }
        var losses: Int { all.filter { $0 == .ended(.lost(retrying: true)) }.count }
    }

    func collect(_ s: any TabTerminalSource) -> Events {
        let e = Events()
        Task { @MainActor in
            for await ev in s.events { e.all.append(ev) }
        }
        return e
    }

    /// At least `limit`, counted in turns rather than by the clock: another
    /// test can hold the main actor for seconds (ControlModeTabsTests).
    func waitUntil(_ limit: Duration, _ cond: @MainActor () -> Bool) async {
        let turns = Int(limit / .milliseconds(20))
        for _ in 0..<turns where !cond() { try? await Task.sleep(for: .milliseconds(20)) }
    }

    /// A live, quiet stream whose portenvd freezes: keepalive's unanswered
    /// ping ends it within time + timeout, the view says it's reconnecting,
    /// and once portenvd runs again the stream comes back with a reset.
    @Test(.timeLimit(.minutes(2))) func aFrozenPortenvdEndsALiveStream() async throws {
        let fake = try await FakePortenvd.start()
        defer { fake.stop() }
        let client = try DaemonClient(socket: URL(fileURLWithPath: fake.proxy.path), liveness: Self.liveness)
        let s = ControlModeTabProvider(box: "b", transport: client, retry: Self.retry).open(tab: "@1", size: size)
        defer { s.close() }
        let e = collect(s)
        await waitUntil(.seconds(10)) { e.resets == 1 }
        try #require(e.resets == 1)

        // Without keepalive the stream waits forever (mutation-checked).
        fake.proxy.frozen = true
        await waitUntil(.seconds(15)) { e.losses >= 1 }
        #expect(e.losses >= 1, "a frozen portenvd must end the stream")

        fake.proxy.frozen = false
        await waitUntil(.seconds(15)) { e.resets >= 2 }
        #expect(e.resets >= 2, "back once portenvd runs again")
    }

    /// A stream opened to a portenvd that's already frozen: the connection
    /// is accepted and never answers, so no keepalive runs on it; the
    /// source's own deadline for the first reset gives it up.
    @Test(.timeLimit(.minutes(2))) func aStreamOpenedToAFrozenPortenvdIsGivenUp() async throws {
        let fake = try await FakePortenvd.start()
        defer { fake.stop() }
        fake.proxy.frozen = true
        let client = try DaemonClient(socket: URL(fileURLWithPath: fake.proxy.path), liveness: Self.liveness)
        let s = ControlModeTabProvider(box: "b", transport: client, retry: Self.retry).open(tab: "@1", size: size)
        defer { s.close() }
        let e = collect(s)
        await waitUntil(.seconds(8)) { e.losses >= 1 }
        #expect(e.losses >= 1)
        #expect((s as? ControlModeTabSource)?.failure == ControlModeTabSource.notAnswering)
        #expect(e.resets == 0)
        fake.proxy.frozen = false
        await waitUntil(.seconds(15)) { e.resets >= 1 }
        #expect(e.resets >= 1)
    }
}

/// portenvd's TabTerminal and nothing else, on a Unix socket, behind a
/// proxy the test can freeze.
final class FakePortenvd: Sendable {
    let server: GRPCServer<HTTP2ServerTransport.Posix>
    let proxy: FreezingProxy
    let running: Task<Void, Never>
    let socket: String

    static func start() async throws -> FakePortenvd {
        let dir = FileManager.default.temporaryDirectory.path
        let id = UUID().uuidString.prefix(8)
        let socket = "\(dir)/pf-\(id).sock"
        let transport = HTTP2ServerTransport.Posix(address: .unixDomainSocket(path: socket), transportSecurity: .plaintext)
        let server = GRPCServer(transport: transport, services: [TabTerminalOnly()])
        let running = Task<Void, Never> { _ = try? await server.serve() }
        _ = try await transport.listeningAddress
        let proxy = try FreezingProxy(path: "\(dir)/pp-\(id).sock", upstream: socket)
        return FakePortenvd(server: server, proxy: proxy, running: running, socket: socket)
    }

    private init(server: GRPCServer<HTTP2ServerTransport.Posix>, proxy: FreezingProxy, running: Task<Void, Never>, socket: String) {
        self.server = server
        self.proxy = proxy
        self.running = running
        self.socket = socket
    }

    func stop() {
        proxy.stop()
        server.beginGracefulShutdown()
        running.cancel()
        unlink(socket)
    }
}

/// Answers TabTerminal with a reset, then nothing until the app ends it.
struct TabTerminalOnly: RegistrableRPCService {
    func registerMethods<Transport: ServerTransport>(with router: inout RPCRouter<Transport>) {
        router.registerHandler(
            forMethod: Portenv_Daemon_V1_DaemonService.Method.TabTerminal.descriptor,
            deserializer: ProtoDeserializer<Portenv_Daemon_V1_TabTerminalRequest>(),
            serializer: ProtoSerializer<Portenv_Daemon_V1_TabTerminalResponse>()
        ) { request, _ in
            StreamingServerResponse { writer in
                try await writer.write(.with {
                    $0.reset = .with { $0.size = .with { $0.cols = 80; $0.rows = 24 } }
                })
                for try await _ in request.messages {}
                return [:]
            }
        }
    }
}

struct ProtoSerializer<M: SwiftProtobuf.Message>: MessageSerializer {
    func serialize<Bytes: GRPCContiguousBytes>(_ message: M) throws -> Bytes {
        Bytes(try message.serializedBytes() as [UInt8])
    }
}

struct ProtoDeserializer<M: SwiftProtobuf.Message>: MessageDeserializer {
    func deserialize<Bytes: GRPCContiguousBytes>(_ serializedMessageBytes: Bytes) throws -> M {
        try serializedMessageBytes.withUnsafeBytes { try M(serializedBytes: [UInt8]($0)) }
    }
}

/// A Unix-socket proxy that, while `frozen`, passes no bytes either way:
/// what a stopped portenvd looks like from the app.
final class FreezingProxy: @unchecked Sendable {
    let path: String
    private let upstream: String
    private let lock = NSLock()
    private var _frozen = false
    private var stopped = false
    private var fds: [Int32] = []
    private let listener: Int32

    var frozen: Bool {
        get { lock.withLock { _frozen } }
        set { lock.withLock { _frozen = newValue } }
    }

    init(path: String, upstream: String) throws {
        self.path = path
        self.upstream = upstream
        unlink(path)
        listener = socket(AF_UNIX, SOCK_STREAM, 0)
        guard listener >= 0, Self.withAddress(path, { bind(listener, $0, $1) }) == 0, listen(listener, 16) == 0 else {
            throw POSIXError(.EADDRINUSE)
        }
        Thread.detachNewThread { [self] in acceptLoop() }
    }

    func stop() {
        let all = lock.withLock { () -> [Int32] in
            stopped = true
            return fds
        }
        shutdown(listener, SHUT_RDWR)
        close(listener)
        for fd in all { shutdown(fd, SHUT_RDWR); close(fd) }
        unlink(path)
    }

    private func acceptLoop() {
        while true {
            let client = accept(listener, nil, nil)
            if client < 0 { return }
            let up = socket(AF_UNIX, SOCK_STREAM, 0)
            guard Self.withAddress(upstream, { connect(up, $0, $1) }) == 0 else {
                close(client)
                close(up)
                continue
            }
            let keep = lock.withLock { () -> Bool in
                if stopped { return false }
                fds += [client, up]
                return true
            }
            if !keep {
                close(client)
                close(up)
                return
            }
            Thread.detachNewThread { [self] in pump(from: client, to: up) }
            Thread.detachNewThread { [self] in pump(from: up, to: client) }
        }
    }

    /// Holds what it read while frozen; passes it on once thawed.
    private func pump(from: Int32, to: Int32) {
        var buf = [UInt8](repeating: 0, count: 65536)
        while true {
            while frozen {
                if lock.withLock({ stopped }) { return }
                usleep(10000)
            }
            let n = read(from, &buf, buf.count)
            if n <= 0 { shutdown(to, SHUT_WR); return }
            while frozen {
                if lock.withLock({ stopped }) { return }
                usleep(10000)
            }
            var off = 0
            while off < n {
                let w = buf[off..<n].withUnsafeBytes { write(to, $0.baseAddress, $0.count) }
                if w <= 0 { return }
                off += w
            }
        }
    }

    private static func withAddress(_ path: String, _ body: (UnsafePointer<sockaddr>, socklen_t) -> Int32) -> Int32 {
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { return -1 }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        return withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { body($0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
    }
}
