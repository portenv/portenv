// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation

/// portenvd's call that takes a made key, in memory only.
public protocol ServerKeyHandover: Sendable {
    func provideServerKey(_ id: String, seed: Data) async throws
}

/// A key Portenv makes just for one server (ADR 0021): Ed25519, made with
/// CryptoKit; its seed in the Keychain (service dev.portenv.server-key, a
/// lane's own suffix; account the server's ID; never synchronised,
/// this-device-only, with ADR 0019's caveat for the file-based login
/// keychain), its public half shown to paste into the server. It signs in
/// to that server only, for SSH only.
public enum ServerSignInKey {
    public struct Made: Sendable {
        /// "ssh-ed25519 AAAA… portenv-<server id>": no user, host or email.
        public let publicLine: String
    }

    public static var store: SystemSecretStore { SystemSecretStore(service: Instance.current.serverKeyService) }

    /// Makes the server's key and stores it; refused if it has one. Runs on
    /// the Keychain's queue (`onKeychainQueue`).
    public static func make(serverID: String, store: SecretStore) throws -> Made {
        let key = Curve25519.Signing.PrivateKey()
        try store.add(account: serverID, data: key.rawRepresentation, label: "Portenv server key portenv-\(serverID)")
        return Made(publicLine: publicLine(key.publicKey.rawRepresentation, serverID: serverID))
    }

    /// OpenSSH's one-line form of an Ed25519 public key.
    static func publicLine(_ raw: Data, serverID: String) -> String {
        func string(_ d: Data) -> Data {
            var n = UInt32(d.count).bigEndian
            return Data(bytes: &n, count: 4) + d
        }
        let blob = string(Data("ssh-ed25519".utf8)) + string(raw)
        return "ssh-ed25519 \(blob.base64EncodedString()) portenv-\(serverID)"
    }

    /// At launch: each server that signs in with a made key gets it from the
    /// Keychain (the app, in front of the person, may raise the prompt) and
    /// hands it to portenvd, which holds it in memory. A key that can't be
    /// read is left: that server says it needs the Keychain.
    public static func handOver(to daemon: ServerKeys & ServerKeyHandover, store: SecretStore) async {
        guard let servers = try? await daemon.serverKeys() else { return }
        for server in servers where server.madeKey {
            let id = server.id
            guard let seed = try? await onKeychainQueue({ try store.read(account: id) }) else { continue }
            try? await daemon.provideServerKey(id, seed: seed)
        }
    }
}
