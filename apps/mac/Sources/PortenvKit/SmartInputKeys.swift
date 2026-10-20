// SPDX-License-Identifier: Apache-2.0

import Foundation

// ADR 0027 S5a (R-0353): phase 1's hosted adapters use keys the owner
// enters in Settings › Experimental. They live in the Keychain under their
// own service, which portenvd, the CLI and the helper never read. The app
// reads a key only when it makes a call; it's never logged, never shown
// back and never sent anywhere but its own service's header.

/// A hosted adapter's key.
public enum ProviderKeyID: String, CaseIterable, Sendable {
    case anthropic, jev

    /// The Keychain item's account.
    public var account: String { rawValue }

    /// The item's label in Keychain Access.
    public var label: String {
        switch self {
        case .anthropic: "Portenv smart input: Anthropic API key"
        case .jev: "Portenv smart input: jev API key"
        }
    }
}

/// Where the keys are kept. The app's is the Keychain; tests use one in
/// memory, so they never raise a Keychain prompt.
public protocol ProviderKeyStore: Sendable {
    /// The key, or nil when none is saved.
    func key(_ id: ProviderKeyID) async throws -> String?
    func save(_ key: String, for id: ProviderKeyID) async throws
    func remove(_ id: ProviderKeyID) async throws
    /// When the key was saved (its attributes only, never its data).
    func savedOn(_ id: ProviderKeyID) async throws -> Date?
}

/// What the owner pastes, before it reaches the Keychain.
public enum ProviderKeyEntry {
    public static let maxLength = 1024

    /// The key trimmed, or nil when it can't be one (empty, spaces inside,
    /// control characters, too long).
    public static func clean(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, t.count <= maxLength,
              !t.unicodeScalars.contains(where: { CharacterSet.whitespacesAndNewlines.contains($0) || CharacterSet.controlCharacters.contains($0) })
        else { return nil }
        return t
    }
}

/// The keys in the login keychain, under `Instance.smartInputKeyService`.
/// Calls run on the Keychain queue, never the main thread or Swift's
/// cooperative pool. Only the app, in front of the person, uses this.
public struct KeychainProviderKeys: ProviderKeyStore {
    private let store: SystemSecretStore

    public init(service: String = Instance.current.smartInputKeyService) {
        store = SystemSecretStore(service: service)
    }

    private func onQueue<T: Sendable>(_ body: @escaping @Sendable () throws -> T) async throws -> T {
        try await withCheckedThrowingContinuation { c in
            KeychainWait.queue.async { c.resume(with: Result { try body() }) }
        }
    }

    public func key(_ id: ProviderKeyID) async throws -> String? {
        let store = store
        return try await onQueue { try store.read(account: id.account).map { String(decoding: $0, as: UTF8.self) } }
    }

    public func save(_ key: String, for id: ProviderKeyID) async throws {
        let store = store
        // A new item each time, so "Saved on" is when this key was saved.
        try await onQueue {
            try store.delete(account: id.account)
            try store.add(account: id.account, data: Data(key.utf8), label: id.label)
        }
    }

    public func remove(_ id: ProviderKeyID) async throws {
        let store = store
        try await onQueue { try store.delete(account: id.account) }
    }

    public func savedOn(_ id: ProviderKeyID) async throws -> Date? {
        let store = store
        return try await onQueue { try store.createdAt(account: id.account) }
    }
}

extension ProviderKeyStore {
    /// The key for a call: missing or unreadable is `.noKey`, so the
    /// adapter is skipped and the line falls through.
    func required(_ id: ProviderKeyID) async throws -> String {
        guard let k = try? await key(id), !k.isEmpty else { throw AdapterFailure.noKey }
        return k
    }
}
