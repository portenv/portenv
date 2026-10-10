// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security

/// Reads a box's keys from the Keychain. Only the app does this, in front of
/// the person (PLAN.md, Phase 1 item 1): portenvd never raises a Keychain
/// prompt, it asks the app ("Keychain needs your approval").
public protocol KeychainReading: Sendable {
    /// The key stored under account (service dev.portenv.repository), or nil.
    /// May block while macOS asks the person to approve: never call it on
    /// the main thread.
    func read(account: String) throws -> Data?
}

/// The person couldn't or didn't approve a Keychain read.
public struct KeychainError: Error, LocalizedError, Equatable {
    public let status: OSStatus
    public var errorDescription: String? { "The Keychain didn't give Portenv the box's keys (\(status))." }
}

/// The macOS Keychain, read with the system's approval prompt allowed.
public struct SystemKeychain: KeychainReading {
    public init() {}

    public func read(account: String) throws -> Data? {
        precondition(!Thread.isMainThread, "Keychain reads never run on the main thread")
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: "dev.portenv.repository",
            kSecAttrAccount as String: account,
            kSecMatchLimit as String: kSecMatchLimitOne,
            kSecReturnData as String: true,
        ]
        var out: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &out)
        switch status {
        case errSecSuccess: return out as? Data
        case errSecItemNotFound: return nil
        default: throw KeychainError(status: status)
        }
    }
}

/// Posts the notification for a Keychain wait (asks for permission then,
/// never at launch; denied, nothing nags).
public protocol KeychainWaitNotifying: Sendable {
    func keychainWaiting(box: String) async
    /// A box left unsaved by Quit Anyway was saved in the background.
    func savedAfterQuit(box: String, at: Date) async
    /// A move finished; the notifier decides whether it's worth a
    /// notification (longer than 10 s, window not in front: §5).
    func moveFinished(box: String, to: String, seconds: Int) async
}

public extension KeychainWaitNotifying {
    func moveFinished(box: String, to: String, seconds: Int) async {}
}

extension KeychainWaitNotifying {
    public func savedAfterQuit(box _: String, at _: Date) async {}
}

/// The background-save notification's text (GUIDELINES.md §5: "<box> ·
/// <event>", one line of result).
public enum SavedAfterQuitNote {
    public static func title(box: String) -> String { "\(box) · Saved after Portenv quit" }
    public static func body(at: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        "Saved at \(StateLine.clock(at, timeZone: timeZone, locale: locale)) and closed. Nothing was lost."
    }
}

/// The texts of the Keychain wait (GUIDELINES.md §3.1).
public enum KeychainWait {
    /// Where Keychain reads run: a concurrent queue of their own. A read
    /// waits as long as macOS's prompt is up, so it must never hold a thread
    /// of Swift's cooperative pool; and no read waits behind another (a read
    /// cancelled while its prompt is still up must not block the next).
    public static let queue = DispatchQueue(label: "com.portenv.keychain", qos: .userInitiated, attributes: .concurrent)

    public static let line = "Waiting for Keychain access"
    public static let detail = "Check for a password prompt. It may be behind other windows."
    /// The error portenvd returns when a key needs the person's approval.
    public static let daemonError = "Keychain needs your approval. Open Portenv on this Mac to allow it."
    /// How long a read may take before the window says it's waiting.
    public static let threshold: Duration = .seconds(2)
}

/// Delivers one value once: the first of a Keychain read finishing and the
/// person cancelling wins.
final class Once<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var deliver: (@Sendable (T) -> Void)?
    private var done = false

    func set(_ deliver: @escaping @Sendable (T) -> Void) { lock.withLock { self.deliver = deliver } }

    var isDone: Bool { lock.withLock { done } }

    func resume(_ value: T) {
        let d: (@Sendable (T) -> Void)? = lock.withLock {
            guard !done else { return nil }
            done = true
            return deliver
        }
        d?(value)
    }
}
