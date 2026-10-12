// SPDX-License-Identifier: Apache-2.0

import Foundation

/// One box ID and its recovery password, derived ahead of time (ADR 0019).
/// It never prints its password.
public struct PoolEntry: Codable, Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let boxID: String
    public let password: String

    public init(boxID: String, password: String) {
        self.boxID = boxID
        self.password = password
    }

    enum CodingKeys: String, CodingKey {
        case boxID = "box_id"
        case password
    }

    public var description: String { "[pool entry \(boxID)]" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: ["boxID": boxID], displayStyle: .struct) }
}

/// The pool of pre-derived recovery passwords (ADR 0019): filled while
/// first run still holds the recovery key, so the app adds a recovery key
/// to every box it makes without ever keeping the recovery key.
public enum RecoveryPool {
    public static let size = 32
    /// The one Keychain item holding the whole pool.
    public static let account = "pool"
    public static let label = "Portenv recovery pool"

    /// A new box ID: "box-" and 12 hex digits from 6 random bytes, the
    /// format of portenv init's.
    static func newBoxID(randomBytes: (Int) throws -> [UInt8]) throws -> String {
        let b = try randomBytes(6)
        guard b.count == 6 else { throw RecoveryKey.GenerateError(message: "This Mac couldn't make random numbers for the recovery key.") }
        return "box-" + b.map { String(format: "%02x", $0) }.joined()
    }

    /// `count` entries with distinct new box IDs, each with its password
    /// derived from `key`.
    public static func entries(from key: RecoveryKey, count: Int = size,
                               randomBytes: (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes) throws -> [PoolEntry] {
        var seen = Set<String>()
        var out: [PoolEntry] = []
        // A good source repeats a 48-bit ID practically never; a source that
        // keeps repeating is broken.
        var tries = 0
        while out.count < count {
            tries += 1
            guard tries <= count * 4 else {
                throw RecoveryKey.GenerateError(message: "This Mac couldn't make random numbers for the recovery key.")
            }
            let id = try newBoxID(randomBytes: randomBytes)
            guard seen.insert(id).inserted else { continue }
            out.append(PoolEntry(boxID: id, password: try key.password(boxID: id)))
        }
        return out
    }
}

/// The pool in its Keychain item. Every call may wait for the Keychain:
/// run it with `onKeychainQueue`.
public struct RecoveryPoolStore: Sendable {
    /// The item is there but isn't a pool this app wrote.
    public struct Unreadable: Error, LocalizedError, Equatable {
        public var errorDescription: String? { "Portenv couldn't read its recovery passwords in the Keychain." }
    }

    private struct Stored: Codable {
        var version = 1
        var entries: [PoolEntry]
    }

    let store: SecretStore

    public init(store: SecretStore) { self.store = store }

    /// The entries left, in the order they're used.
    public func entries() throws -> [PoolEntry] {
        guard let data = try store.read(account: RecoveryPool.account) else { return [] }
        guard let s = try? JSONDecoder().decode(Stored.self, from: data), s.version == 1 else { throw Unreadable() }
        return s.entries
    }

    /// A new pool from the recovery key, replacing any pool there: the old
    /// item is deleted and a new one added (an update would keep the old
    /// item's access list). Nothing is written unless all 32 entries were
    /// made. The key is wiped afterwards, whatever happens: it lives in
    /// memory only until the pool is filled (ADR 0019).
    public func fill(from key: inout RecoveryKey, randomBytes: (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes) throws {
        defer { key.wipe() }
        let data = try JSONEncoder().encode(Stored(entries: RecoveryPool.entries(from: key, randomBytes: randomBytes)))
        try store.delete(account: RecoveryPool.account)
        try store.add(account: RecoveryPool.account, data: data, label: RecoveryPool.label)
    }

    /// Removes the entries for these box IDs (each box made, or never to be
    /// used again).
    public func remove(boxIDs: Set<String>) throws {
        let all = try entries()
        let left = all.filter { !boxIDs.contains($0.boxID) }
        guard left.count != all.count else { return }
        try write(left)
    }

    /// Forgets the pool (Settings, after a suspected compromise; Re-encrypt).
    /// Refilling it needs the recovery key.
    public func discard() throws {
        try store.delete(account: RecoveryPool.account)
    }

    private func write(_ entries: [PoolEntry]) throws {
        let data = try JSONEncoder().encode(Stored(entries: entries))
        try store.set(account: RecoveryPool.account, data: data, label: RecoveryPool.label)
    }
}
