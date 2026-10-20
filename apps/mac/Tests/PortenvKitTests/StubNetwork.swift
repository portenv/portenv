// SPDX-License-Identifier: Apache-2.0

import Foundation

@testable import PortenvKit

/// The network in tests (ADR 0027, S5a): a URLSession whose every request
/// is answered by the test's own handler, never the internet. Each session
/// carries its own ID, so tests running in parallel never share a handler;
/// a request without a known ID fails, so nothing can reach a real host.
final class StubNetwork: @unchecked Sendable {
    struct Seen: Sendable {
        let url: URL
        let method: String
        let headers: [String: String]
        let body: Data
    }

    enum Reply: Sendable {
        case json(Int, String)
        case delayed(Duration, Int, String)
        case fail(URLError.Code)
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var all: [String: StubNetwork] = [:]
    static func find(_ id: String) -> StubNetwork? { lock.withLock { all[id] } }

    let id = UUID().uuidString
    private let lock = NSLock()
    private var _seen: [Seen] = []
    private let reply: @Sendable (Seen) -> Reply

    init(_ reply: @escaping @Sendable (Seen) -> Reply) {
        self.reply = reply
        Self.lock.withLock { Self.all[id] = self }
    }

    convenience init(_ fixed: Reply) { self.init { _ in fixed } }

    deinit { Self.lock.withLock { _ = Self.all.removeValue(forKey: id) } }

    var seen: [Seen] { lock.withLock { _seen } }

    /// An ephemeral session that only this stub answers.
    var session: URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [StubProtocol.self]
        c.httpAdditionalHeaders = [StubProtocol.header: id]
        return URLSession(configuration: c)
    }

    fileprivate func answer(_ s: Seen) -> Reply {
        lock.withLock { _seen.append(s) }
        return reply(s)
    }
}

final class StubProtocol: URLProtocol, @unchecked Sendable {
    static let header = "X-Portenv-Test-Stub"

    override class func canInit(with _: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let r = request
        guard let id = r.value(forHTTPHeaderField: Self.header), let stub = StubNetwork.find(id), let url = r.url else {
            client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
            return
        }
        var headers = r.allHTTPHeaderFields ?? [:]
        headers.removeValue(forKey: Self.header)
        let seen = StubNetwork.Seen(url: url, method: r.httpMethod ?? "GET", headers: headers, body: Self.body(r))
        let send: @Sendable (Int, String) -> Void = { [weak self] status, text in
            guard let self else { return }
            let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(text.utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
        switch stub.answer(seen) {
        case let .json(status, text): send(status, text)
        case let .delayed(d, status, text):
            let c = d.components
            let seconds = Double(c.seconds) + Double(c.attoseconds) / 1e18
            DispatchQueue.global().asyncAfter(deadline: .now() + seconds) { send(status, text) }
        case let .fail(code): client?.urlProtocol(self, didFailWithError: URLError(code))
        }
    }

    override func stopLoading() {}

    private static func body(_ r: URLRequest) -> Data {
        if let b = r.httpBody { return b }
        guard let s = r.httpBodyStream else { return Data() }
        s.open()
        defer { s.close() }
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        while s.hasBytesAvailable {
            let n = s.read(&buf, maxLength: buf.count)
            if n <= 0 { break }
            out.append(buf, count: n)
        }
        return out
    }
}

/// A key store in memory: tests never touch the Keychain
/// (PORTENV_TEST_KEYCHAIN stays unset).
final class FakeKeys: ProviderKeyStore, @unchecked Sendable {
    private let lock = NSLock()
    private var keys: [ProviderKeyID: String]
    private var _reads = 0
    var reads: Int { lock.withLock { _reads } }
    init(_ keys: [ProviderKeyID: String] = [:]) { self.keys = keys }

    func key(_ id: ProviderKeyID) async throws -> String? { lock.withLock { _reads += 1; return keys[id] } }
    func save(_ key: String, for id: ProviderKeyID) async throws { lock.withLock { keys[id] = key } }
    func remove(_ id: ProviderKeyID) async throws { lock.withLock { _ = keys.removeValue(forKey: id) } }
    func savedOn(_ id: ProviderKeyID) async throws -> Date? { lock.withLock { keys[id] == nil ? nil : Date(timeIntervalSince1970: 0) } }
}
