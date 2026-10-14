// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Answers like portenvd's ListServers, ScanServerKey and TrustServerKey.
private final class FakeServerKeys: ServerKeys, @unchecked Sendable {
    private let lock = NSLock()
    var listed: [ServerKeyInfo]
    /// What a scan of each server finds; a missing one fails (unreachable).
    var scans: [String: ServerKeyInfo]
    private(set) var calls: [String] = []

    init(listed: [ServerKeyInfo], scans: [String: ServerKeyInfo] = [:]) {
        self.listed = listed
        self.scans = scans
    }

    func serverKeys() async throws -> [ServerKeyInfo] {
        lock.withLock { calls.append("list") }
        return listed
    }

    func scanServerKey(_ id: String) async throws -> ServerKeyInfo {
        lock.withLock { calls.append("scan \(id)") }
        guard let s = scans[id] else { throw DaemonError("couldn't reach test-server to read its key: Connection refused") }
        return s
    }

    func trustServerKey(_ id: String, fingerprint: String) async throws -> ServerKeyInfo {
        lock.withLock { calls.append("trust \(id) \(fingerprint)") }
        return ServerKeyInfo(id: id, name: "test-server", state: .confirmed, fingerprint: fingerprint)
    }
}

@MainActor
struct ServerKeyCheckTests {
    private let fp = "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s"

    private func waiting(_ id: String = "srv-0123456789ab", name: String = "test-server") -> ServerKeyInfo {
        ServerKeyInfo(id: id, name: name, state: .unconfirmed, fingerprint: "")
    }

    /// An unconfirmed server is scanned and asked about, with its
    /// fingerprint; Trust pins exactly the fingerprint shown.
    @Test func trustPinsTheFingerprintShown() async {
        let keys = FakeServerKeys(listed: [waiting()], scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: fp, keyType: "ssh-ed25519")])
        var asked: [ServerKeyQuestion] = []
        await ServerKeyCheck(keys: keys).run(ask: { q in asked.append(q); return true }, tell: { _ in Issue.record("told") })
        #expect(asked.count == 1)
        #expect(asked.first?.title == "Is this test-server?")
        // The fingerprint on a line of its own, never wrapped into the text.
        #expect(asked.first?.fingerprint == fp)
        #expect(asked.first?.keyName == "ED25519 key")
        #expect(asked.first?.detail.contains("SHA256") == false)
        #expect(keys.calls == ["list", "scan srv-0123456789ab", "trust srv-0123456789ab \(fp)"])
    }

    /// Not Now leaves it unconfirmed: nothing is trusted.
    @Test func notNowTrustsNothing() async {
        let keys = FakeServerKeys(listed: [waiting()], scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: fp, keyType: "ssh-ed25519")])
        await ServerKeyCheck(keys: keys).run(ask: { _ in false }, tell: { _ in })
        #expect(!keys.calls.contains { $0.hasPrefix("trust") })
    }

    /// The same key in this Mac's SSH known hosts confirms it silently.
    @Test func aMatchInKnownHostsAsksNothing() async {
        let keys = FakeServerKeys(listed: [waiting()], scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .confirmed, fingerprint: fp, fromKnownHosts: true)])
        await ServerKeyCheck(keys: keys).run(ask: { _ in Issue.record("asked"); return true }, tell: { _ in Issue.record("told") })
        #expect(keys.calls == ["list", "scan srv-0123456789ab"])
    }

    /// Another key in known hosts: said, with no Trust (R-0180).
    @Test func aMismatchOnlyTells() async {
        let keys = FakeServerKeys(listed: [waiting()], scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .changed, fingerprint: "")])
        var told: [ServerKeyNotice] = []
        await ServerKeyCheck(keys: keys).run(ask: { _ in Issue.record("asked"); return true }, tell: { told.append($0) })
        #expect(told == [ServerKeyNotice(title: "test-server’s key doesn’t match", detail: "This server’s key doesn’t match the one this Mac’s SSH recorded. Check with whoever runs it.")])
        #expect(!keys.calls.contains { $0.hasPrefix("trust") })
    }

    /// A key that changed since it was confirmed: said, no Trust, no scan.
    @Test func aChangedKeyOnlyTells() async {
        let keys = FakeServerKeys(listed: [ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .changed, fingerprint: "")])
        var told: [ServerKeyNotice] = []
        await ServerKeyCheck(keys: keys).run(ask: { _ in Issue.record("asked"); return true }, tell: { told.append($0) })
        #expect(told.first?.title == "test-server’s key has changed")
        // Until the app can forget a key, the owner's way past it (R-0183).
        #expect(told.first?.detail.contains("portenv app forget-server test-server") == true)
        #expect(keys.calls == ["list"])
    }

    /// Confirmed servers aren't scanned; one that can't be reached is left
    /// for the next launch, and the others are still asked about.
    @Test func onlyWaitingServersAndAnUnreachableOneIsSkipped() async {
        let other = waiting("srv-ba9876543210", name: "build-box")
        let keys = FakeServerKeys(
            listed: [ServerKeyInfo(id: "srv-111111111111", name: "done", state: .confirmed, fingerprint: fp), waiting(), other],
            scans: ["srv-ba9876543210": ServerKeyInfo(id: "srv-ba9876543210", name: "build-box", state: .unconfirmed, fingerprint: fp, keyType: "ecdsa-sha2-nistp256")]
        )
        var asked: [String] = []
        await ServerKeyCheck(keys: keys).run(ask: { asked.append($0.title); return false }, tell: { _ in })
        #expect(keys.calls == ["list", "scan srv-0123456789ab", "scan srv-ba9876543210"])
        #expect(asked == ["Is this build-box?"])
    }

    /// At most once per launch, however often launch runs (Try Again).
    @Test func onceAPerLaunch() async {
        let keys = FakeServerKeys(listed: [waiting()], scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: fp, keyType: "ssh-ed25519")])
        let check = ServerKeyCheck(keys: keys)
        var asked = 0
        await check.run(ask: { _ in asked += 1; return false }, tell: { _ in })
        await check.run(ask: { _ in asked += 1; return false }, tell: { _ in })
        #expect(asked == 1)
    }

    /// A key that changed and was forgotten (R-0185): the question opens
    /// with that, and Trust still sends exactly the fingerprint shown.
    @Test func aForgottenChangedKeySaysSoFirst() async {
        let keys = FakeServerKeys(listed: [ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: "", wasChanged: true)],
                                  scans: ["srv-0123456789ab": ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: fp, keyType: "ssh-ed25519", wasChanged: true)])
        var asked: [ServerKeyQuestion] = []
        await ServerKeyCheck(keys: keys).run(ask: { asked.append($0); return true }, tell: { _ in Issue.record("told") })
        #expect(asked.first?.detail.hasPrefix("This server’s key changed since you last confirmed it. Check the fingerprint with whoever runs it.") == true)
        #expect(asked.first?.fingerprint == fp)
        #expect(keys.calls.last == "trust srv-0123456789ab \(fp)")
    }

    /// Without the mark, no such line.
    @Test func aFirstConfirmationHasNoChangedLine() {
        let q = ServerKeyQuestion(ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed, fingerprint: fp, keyType: "ssh-ed25519"))
        #expect(!q.detail.contains("changed"))
    }
}
