// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation
import Testing

@testable import PortenvKit

/// Takes made keys, as portenvd's ProvideServerKey does.
private final class FakeHandover: ServerKeys, ServerKeyHandover, @unchecked Sendable {
    private let lock = NSLock()
    let listed: [ServerKeyInfo]
    private(set) var provided: [String: Data] = [:]
    init(listed: [ServerKeyInfo]) { self.listed = listed }
    func serverKeys() async throws -> [ServerKeyInfo] { listed }
    func scanServerKey(_: String) async throws -> ServerKeyInfo { throw DaemonError("no") }
    func trustServerKey(_: String, fingerprint _: String) async throws -> ServerKeyInfo { throw DaemonError("no") }
    func provideServerKey(_ id: String, seed: Data) async throws { lock.withLock { provided[id] = seed } }
}

struct ServerSignInKeyTests {
    /// A made key (ADR 0021): Ed25519, its public half in OpenSSH's format
    /// with the comment portenv-<server id> (no user, host or email), and
    /// its seed stored in the server-key service under the server's ID.
    @Test func aMadeKeyIsStoredUnderTheServersID() throws {
        let store = FakeSecretStore()
        let made = try ServerSignInKey.make(serverID: "srv-0123456789ab", store: store)
        let parts = made.publicLine.split(separator: " ")
        #expect(parts.count == 3 && parts[0] == "ssh-ed25519" && parts[2] == "portenv-srv-0123456789ab")
        let blob = try #require(Data(base64Encoded: String(parts[1])))
        // string "ssh-ed25519", then string <32-byte key>
        #expect(blob.count == 4 + 11 + 4 + 32)
        #expect(blob.prefix(15) == Data([0, 0, 0, 11]) + Data("ssh-ed25519".utf8))
        let seed = try #require(store.all["srv-0123456789ab"])
        #expect(seed.count == 32)
        let pub = try Curve25519.Signing.PrivateKey(rawRepresentation: seed).publicKey.rawRepresentation
        #expect(blob.suffix(32) == pub)
        #expect(store.label("srv-0123456789ab") == "Portenv server key portenv-srv-0123456789ab")
        // One key per server: a second make for it is refused, the first kept.
        #expect(throws: (any Error).self) { try ServerSignInKey.make(serverID: "srv-0123456789ab", store: store) }
        #expect(store.all["srv-0123456789ab"] == seed)
    }

    @Test func theServiceIsItsOwnAndPerLane() throws {
        #expect(try Instance(info: [:]).serverKeyService == "dev.portenv.server-key")
        #expect(try Instance(info: ["PortenvInstance": "dev-C"]).serverKeyService == "dev.portenv.server-key.dev-C")
    }

    /// At launch, the made keys of servers that sign in with one are read and
    /// handed to portenvd, in memory; no other server's item is read.
    @Test func madeKeysAreHandedOver() async throws {
        let store = FakeSecretStore()
        _ = try ServerSignInKey.make(serverID: "srv-111111111111", store: store)
        let daemon = FakeHandover(listed: [
            ServerKeyInfo(id: "srv-111111111111", name: "made", state: .confirmed, fingerprint: "", madeKey: true),
            ServerKeyInfo(id: "srv-222222222222", name: "mine", state: .confirmed, fingerprint: ""),
        ])
        await ServerSignInKey.handOver(to: daemon, store: store)
        #expect(daemon.provided == ["srv-111111111111": store.all["srv-111111111111"]!])
        #expect(store.ops == ["add srv-111111111111", "read srv-111111111111"])
    }
}
