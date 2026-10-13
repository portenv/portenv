// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security

/// One Keychain service the app reads and writes (ADR 0019): the boxes'
/// repository keys, or the recovery pool. Only the app uses it, in front of
/// the person; every call may wait for macOS's prompt, so none runs on the
/// main thread (`onKeychainQueue`).
public protocol SecretStore: Sendable {
    /// The item's data, or nil when there's none.
    func read(account: String) throws -> Data?
    /// A new item; an error if one is already there.
    func add(account: String, data: Data, label: String) throws
    /// A new item, or new data for the one there.
    func set(account: String, data: Data, label: String) throws
    /// Deletes the item; nothing there is fine.
    func delete(account: String) throws
}

/// The Keychain refused a write or a delete.
public struct KeychainStoreError: Error, LocalizedError, Equatable {
    public let status: OSStatus
    public init(status: OSStatus) { self.status = status }
    public var errorDescription: String? { "The Keychain refused (\(status))." }
}

/// A Keychain service of the login keychain. Items are written never
/// synchronised and this-device-only (as portenv init's, core/keys), but the
/// file-based login keychain ignores kSecAttrAccessible: until the keys move
/// to the data-protection keychain at 1.8 (PLAN.md), they travel in backups
/// and Migration Assistant like any login keychain item.
public struct SystemSecretStore: SecretStore {
    public let service: String
    public init(service: String) { self.service = service }

    /// The repository keys' service (portenvd and the CLI read the same).
    public static var repositoryKeys: SystemSecretStore { SystemSecretStore(service: Instance.current.keychainService) }
    /// The recovery pool's service: only the app reads it.
    public static var recoveryPool: SystemSecretStore { SystemSecretStore(service: Instance.current.recoveryPoolService) }

    private func base(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
    }

    /// What a new item is written with.
    static func addAttributes(service: String, account: String, data: Data, label: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrLabel as String: label,
            kSecValueData as String: data,
            kSecAttrSynchronizable as String: false,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
        ]
    }

    public func read(account: String) throws -> Data? {
        precondition(!Thread.isMainThread, "Keychain calls never run on the main thread")
        var q = base(account)
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        q[kSecReturnData as String] = true
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        switch status {
        case errSecSuccess: return out as? Data
        case errSecItemNotFound: return nil
        default: throw KeychainStoreError(status: status)
        }
    }

    public func add(account: String, data: Data, label: String) throws {
        precondition(!Thread.isMainThread, "Keychain calls never run on the main thread")
        let status = SecItemAdd(Self.addAttributes(service: service, account: account, data: data, label: label) as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainStoreError(status: status) }
    }

    public func set(account: String, data: Data, label: String) throws {
        precondition(!Thread.isMainThread, "Keychain calls never run on the main thread")
        let status = SecItemUpdate(base(account) as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        switch status {
        case errSecSuccess: return
        case errSecItemNotFound: try add(account: account, data: data, label: label)
        default: throw KeychainStoreError(status: status)
        }
    }

    public func delete(account: String) throws {
        precondition(!Thread.isMainThread, "Keychain calls never run on the main thread")
        let status = SecItemDelete(base(account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainStoreError(status: status) }
    }
}

/// Runs Keychain work on the Keychain queue, never on the cooperative pool:
/// a call can block for as long as macOS's prompt is up. `waiting` hears
/// true once the call has taken longer than `after` (GUIDELINES §3.1:
/// "Waiting for Keychain access"), and false when it's done.
public func onKeychainQueue<T: Sendable>(waiting: (@Sendable (Bool) -> Void)? = nil, after threshold: Duration = KeychainWait.threshold,
                                         _ body: @escaping @Sendable () throws -> T) async throws -> T {
    let state = WaitState()
    let timer = waiting.map { report in
        Task {
            try? await Task.sleep(for: threshold)
            if state.fire() { report(true) }
        }
    }
    defer {
        timer?.cancel()
        state.finish()
        waiting?(false)
    }
    return try await withCheckedThrowingContinuation { continuation in
        KeychainWait.queue.async {
            continuation.resume(with: Result { try body() })
        }
    }
}

/// Whether a Keychain wait was said: once finished, it never is.
private final class WaitState: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func fire() -> Bool { lock.withLock { !done && !Task.isCancelled } }
    func finish() { lock.withLock { done = true } }
}

/// Where a Keychain wait is said: the app's window sets the handler.
public final class KeychainWaitRelay: @unchecked Sendable {
    private let lock = NSLock()
    private var _handler: (@Sendable (Bool) -> Void)?
    public init() {}
    public var handler: (@Sendable (Bool) -> Void)? {
        get { lock.withLock { _handler } }
        set { lock.withLock { _handler = newValue } }
    }
    public func report(_ on: Bool) { handler?(on) }
}
