// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Where a server's host key stands (ADR 0021, R-0180). A server that isn't
/// confirmed is never contacted; a changed key is a hard stop.
public struct ServerKeyInfo: Equatable, Sendable {
    public enum State: Equatable, Sendable { case unconfirmed, confirmed, changed }
    /// The server's ID: never its address.
    public let id: String
    /// The name it shows by (GUIDELINES §2).
    public let name: String
    public let state: State
    /// SHA256:…, the pinned key's or, while it waits, the last scan's.
    public let fingerprint: String
    /// Confirmed because this Mac's SSH known hosts had the same key.
    public let fromKnownHosts: Bool
    /// ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa…: from a scan only.
    public let keyType: String
    /// It signs in with a key Portenv made for it.
    public let madeKey: Bool

    public init(id: String, name: String, state: State, fingerprint: String, fromKnownHosts: Bool = false, keyType: String = "", madeKey: Bool = false) {
        self.id = id
        self.name = name
        self.state = state
        self.fingerprint = fingerprint
        self.fromKnownHosts = fromKnownHosts
        self.keyType = keyType
        self.madeKey = madeKey
    }
}

/// portenvd's calls for servers' host keys.
public protocol ServerKeys: Sendable {
    func serverKeys() async throws -> [ServerKeyInfo]
    /// Finds the key through ssh itself and compares it with this Mac's
    /// SSH known hosts: the same key confirms it, another marks it changed.
    func scanServerKey(_ id: String) async throws -> ServerKeyInfo
    /// Pins the last scan's key, given the fingerprint the person saw.
    func trustServerKey(_ id: String, fingerprint: String) async throws -> ServerKeyInfo
}

/// "Is this test-server?", with its fingerprint: Trust or Not Now. The
/// fingerprint goes on a line of its own, in a fixed-width font, so it's
/// never broken or hyphenated where it wraps.
public struct ServerKeyQuestion: Equatable, Sendable {
    public let title: String
    public let detail: String
    public let fingerprint: String
    /// "ED25519 key", "ECDSA key", "RSA key".
    public let keyName: String
    public static let trust = "Trust"
    public static let notNow = "Not Now"

    public init(_ info: ServerKeyInfo) {
        title = "Is this \(info.name)?"
        detail = "Portenv hasn’t confirmed this server yet, so it won’t connect to it or open its workspaces. Trust it only if this fingerprint matches the one its provider, or whoever runs it, shows you."
        fingerprint = info.fingerprint
        keyName = "\(Self.typeName(info.keyType)) key"
    }

    /// The key type as people read it: ED25519, ECDSA, RSA.
    static func typeName(_ type: String) -> String {
        switch type {
        case "ssh-ed25519": "ED25519"
        case "ssh-rsa": "RSA"
        case let t where t.hasPrefix("ecdsa-"): "ECDSA"
        default: type
        }
    }
}

/// A server's key that can't be trusted: said, with only OK.
public struct ServerKeyNotice: Equatable, Sendable {
    public let title: String
    public let detail: String

    /// This Mac's SSH recorded another key for it (R-0180).
    public static func mismatch(_ name: String) -> ServerKeyNotice {
        ServerKeyNotice(title: "\(name)’s key doesn’t match",
                        detail: "This server’s key doesn’t match the one this Mac’s SSH recorded. Check with whoever runs it.")
    }

    /// It answered with a key other than the one confirmed.
    public static func changed(_ name: String) -> ServerKeyNotice {
        ServerKeyNotice(title: "\(name)’s key has changed",
                        detail: "Portenv won’t connect to it. This happens when a server is reinstalled, or when someone is in between. Check with whoever runs it. To start again, run portenv app remove-server \(name) in Terminal.")
    }
}

/// At launch, once: each server waiting to be confirmed is scanned and
/// asked about (Trust or Not Now); a key that can't be trusted is said. A
/// server that can't be reached is left for the next launch, and its
/// workspaces say "Confirm <name> to open this."
@MainActor
public final class ServerKeyCheck {
    private let keys: ServerKeys
    private var ran = false

    public init(keys: ServerKeys) { self.keys = keys }

    public func run(ask: (ServerKeyQuestion) async -> Bool, tell: (ServerKeyNotice) async -> Void) async {
        guard !ran else { return }
        ran = true
        guard let servers = try? await keys.serverKeys() else { return }
        for server in servers {
            switch server.state {
            case .confirmed:
                continue
            case .changed:
                await tell(.changed(server.name))
            case .unconfirmed:
                guard let scan = try? await keys.scanServerKey(server.id) else { continue }
                switch scan.state {
                case .confirmed:
                    continue // this Mac's SSH had the same key
                case .changed:
                    await tell(.mismatch(scan.name))
                case .unconfirmed:
                    guard !scan.fingerprint.isEmpty else { continue }
                    guard await ask(ServerKeyQuestion(scan)) else { continue }
                    do {
                        _ = try await keys.trustServerKey(scan.id, fingerprint: scan.fingerprint)
                    } catch {
                        await tell(ServerKeyNotice(title: "Portenv couldn’t trust \(scan.name)’s key",
                                                   detail: (error as? LocalizedError)?.errorDescription ?? error.localizedDescription))
                    }
                }
            }
        }
    }
}
