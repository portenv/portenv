// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security
import Testing

@testable import PortenvKit

/// A Keychain service in memory: what the app's code sees of the Keychain,
/// without ever raising a real prompt.
final class FakeSecretStore: SecretStore, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]
    private var labels: [String: String] = [:]
    /// Accounts whose writes fail with this status.
    var failWrites: [String: OSStatus] = [:]
    /// Accounts whose reads give back other bytes than were written.
    var corruptReads: Set<String> = []
    /// Accounts whose deletes fail.
    var failDeletes: Set<String> = []
    /// Accounts whose reads fail (a denied prompt).
    var failReads: Set<String> = []
    private var _ops: [String] = []

    var all: [String: Data] { lock.withLock { items } }
    /// Each call, as "read a", "add a", "set a", "delete a".
    var ops: [String] { lock.withLock { _ops } }
    func label(_ account: String) -> String? { lock.withLock { labels[account] } }

    func read(account: String) throws -> Data? {
        try lock.withLock {
            _ops.append("read \(account)")
            if failReads.contains(account) { throw KeychainStoreError(status: errSecAuthFailed) }
            guard let d = items[account] else { return nil }
            return corruptReads.contains(account) ? Data(d.reversed()) : d
        }
    }

    func add(account: String, data: Data, label: String) throws {
        try lock.withLock {
            _ops.append("add \(account)")
            if let s = failWrites[account] { throw KeychainStoreError(status: s) }
            guard items[account] == nil else { throw KeychainStoreError(status: errSecDuplicateItem) }
            items[account] = data
            labels[account] = label
        }
    }

    func set(account: String, data: Data, label: String) throws {
        try lock.withLock {
            _ops.append("set \(account)")
            if let s = failWrites[account] { throw KeychainStoreError(status: s) }
            items[account] = data
            labels[account] = label
        }
    }

    func delete(account: String) throws {
        try lock.withLock {
            _ops.append("delete \(account)")
            if failDeletes.contains(account) { throw KeychainStoreError(status: errSecIO) }
            items[account] = nil
            labels[account] = nil
        }
    }
}

/// A fixed byte source, so box IDs are known.
func countingBytes(from start: UInt8 = 0) -> @Sendable (Int) throws -> [UInt8] {
    let counter = ByteCounter(start)
    return { n in (0..<n).map { _ in counter.next() } }
}

final class ByteCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var n: UInt8
    init(_ start: UInt8 = 0) { n = start }
    func next() -> UInt8 { lock.withLock { n &+= 1; return n } }
}

let vectorKey = try! RecoveryKey(parsing: "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5")

@Suite struct RecoveryPoolTests {
    /// ADR 0019, test 9: 32 entries from one key, each matching the
    /// derivation, with distinct box IDs in portenv init's format.
    @Test func fillsThirtyTwoEntriesThatMatchTheDerivation() throws {
        let store = FakeSecretStore()
        let pool = RecoveryPoolStore(store: store)
        var key = vectorKey
        try pool.fill(from: &key, randomBytes: countingBytes())
        let entries = try pool.entries()
        #expect(entries.count == RecoveryPool.size)
        #expect(RecoveryPool.size == 32)
        #expect(Set(entries.map(\.boxID)).count == entries.count)
        for e in entries {
            #expect(RecoveryKey.isValidBoxID(e.boxID))
            #expect(e.password == (try vectorKey.password(boxID: e.boxID)))
        }
        #expect(entries[0].boxID == "box-010203040506")
    }

    /// The ADR's own vector: an entry for box-0123456789ab carries the
    /// password in the shared vector file (read from it, not repeated here).
    @Test func anEntryForTheVectorBoxIDCarriesTheVectorPassword() throws {
        let bytes: [UInt8] = [0x01, 0x23, 0x45, 0x67, 0x89, 0xab]
        let entries = try RecoveryPool.entries(from: vectorKey, count: 1, randomBytes: { _ in bytes })
        let vector = try #require(RecoveryKeyTests.vectors.derive.first {
            $0.box_id == "box-0123456789ab" && (try? RecoveryKey(parsing: $0.recovery))?.canonical == vectorKey.canonical
        })
        #expect(entries == [PoolEntry(boxID: "box-0123456789ab", password: vector.derived)])
    }

    /// Test 9: the recovery key isn't in the item, in any form.
    @Test func theRecoveryKeyIsNeverInTheItem() throws {
        let store = FakeSecretStore()
        var key = vectorKey
        try RecoveryPoolStore(store: store).fill(from: &key, randomBytes: countingBytes())
        let data = try #require(store.all[RecoveryPool.account])
        let text = String(decoding: data, as: UTF8.self).lowercased()
        for form in [vectorKey.canonical, vectorKey.grouped, vectorKey.grouped.replacingOccurrences(of: "-", with: " ")] {
            #expect(!text.contains(form.lowercased()))
        }
        #expect(store.label(RecoveryPool.account) == RecoveryPool.label)
    }

    /// Filling again replaces the pool; discarding empties it, and only
    /// the key fills it again.
    @Test func discardingEmptiesItAndRefillingNeedsTheKey() throws {
        let store = FakeSecretStore()
        let pool = RecoveryPoolStore(store: store)
        var key = vectorKey
        try pool.fill(from: &key, randomBytes: countingBytes())
        let first = try pool.entries()
        var again = vectorKey
        try pool.fill(from: &again, randomBytes: countingBytes(from: 100))
        #expect(try pool.entries() != first)
        try pool.discard()
        #expect(try pool.entries().isEmpty)
        #expect(store.all[RecoveryPool.account] == nil)
        try pool.discard() // nothing there: fine
    }

    /// Entries go one by one, only the ones named.
    @Test func removesOnlyTheNamedEntries() throws {
        let store = FakeSecretStore()
        let pool = RecoveryPoolStore(store: store)
        var key = vectorKey
        try pool.fill(from: &key, randomBytes: countingBytes())
        let entries = try pool.entries()
        try pool.remove(boxIDs: [entries[0].boxID, entries[5].boxID, "box-ffffffffffff"])
        let left = try pool.entries()
        #expect(left.count == RecoveryPool.size - 2)
        #expect(left == entries.enumerated().filter { $0.offset != 0 && $0.offset != 5 }.map(\.element))
    }

    /// A pool the app can't read is an error, never an empty pool (which
    /// would only ask for the key again, but would hide a broken item).
    @Test func aCorruptItemIsAnError() throws {
        let store = FakeSecretStore()
        try store.set(account: RecoveryPool.account, data: Data("{".utf8), label: RecoveryPool.label)
        #expect(throws: RecoveryPoolStore.Unreadable.self) { try RecoveryPoolStore(store: store).entries() }
    }

    /// A random source that repeats itself can't fill the pool with
    /// distinct box IDs: an error, nothing written.
    @Test func aRepeatingRandomSourceWritesNothing() throws {
        let store = FakeSecretStore()
        #expect(throws: RecoveryKey.GenerateError.self) {
            var key = vectorKey
            try RecoveryPoolStore(store: store).fill(from: &key, randomBytes: { n in [UInt8](repeating: 7, count: n) })
        }
        #expect(store.all.isEmpty)
    }

    /// A random source that fails stops the fill: nothing is written.
    @Test func aFailingRandomSourceWritesNothing() throws {
        let store = FakeSecretStore()
        struct NoRandom: Error {}
        #expect(throws: NoRandom.self) {
            var key = vectorKey
            try RecoveryPoolStore(store: store).fill(from: &key, randomBytes: { _ in throw NoRandom() })
        }
        #expect(store.all.isEmpty)
    }

    /// Filling replaces an item already there by deleting it and adding a
    /// new one, never by updating it (an update keeps the old item's
    /// access list, which another app may have set).
    @Test func fillDeletesThenAdds() throws {
        let store = FakeSecretStore()
        try store.set(account: RecoveryPool.account, data: Data("{}".utf8), label: "someone else's")
        var key = vectorKey
        try RecoveryPoolStore(store: store).fill(from: &key, randomBytes: countingBytes())
        let writes = store.ops.filter { !$0.hasPrefix("read") }.dropFirst() // the test's own set
        #expect(Array(writes) == ["delete \(RecoveryPool.account)", "add \(RecoveryPool.account)"])
        #expect(store.label(RecoveryPool.account) == RecoveryPool.label)
    }

    /// The recovery key is wiped once the pool is filled (ADR 0019: it lives
    /// in memory only until then), and after a failed fill too.
    @Test func fillWipesTheRecoveryKey() throws {
        let store = FakeSecretStore()
        var key = vectorKey
        try RecoveryPoolStore(store: store).fill(from: &key, randomBytes: countingBytes())
        #expect(key.canonical.isEmpty)
        #expect(throws: RecoveryKey.DeriveError.noKey) { try key.password(boxID: "box-0123456789ab") }
        #expect(!vectorKey.canonical.isEmpty) // another copy is its holder's
        struct NoRandom: Error {}
        var failing = vectorKey
        #expect(throws: NoRandom.self) {
            try RecoveryPoolStore(store: store).fill(from: &failing, randomBytes: { _ in throw NoRandom() })
        }
        #expect(failing.canonical.isEmpty)
    }

    /// Entries never print their password.
    @Test func entriesDontPrintTheirPassword() throws {
        let e = PoolEntry(boxID: "box-0123456789ab", password: try vectorKey.password(boxID: "box-0123456789ab"))
        for text in [String(describing: e), String(reflecting: e), "\(e)", "\([e])"] {
            #expect(!text.contains(e.password))
        }
    }

    /// Test 9: the attributes each new item is written with: never
    /// synchronised, and this-device-only. This checks the dictionary only.
    /// In the file-based login keychain macOS ignores kSecAttrAccessible, so
    /// the items still travel in backups and Migration Assistant until they
    /// move to the data-protection keychain at 1.8 (PLAN.md, R-0122).
    @Test func newItemsAreWrittenNeverSynchronised() throws {
        let q = SystemSecretStore.addAttributes(service: "dev.portenv.recovery-pool", account: RecoveryPool.account,
                                                data: Data("x".utf8), label: RecoveryPool.label)
        #expect(q[kSecAttrSynchronizable as String] as? Bool == false)
        #expect(q[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
        #expect(q[kSecClass as String] as? String == kSecClassGenericPassword as String)
        #expect(q[kSecAttrService as String] as? String == "dev.portenv.recovery-pool")
        #expect(try Instance(info: [:]).recoveryPoolService == "dev.portenv.recovery-pool")
        #expect(try Instance(info: ["PortenvInstance": "dev-C"]).recoveryPoolService == "dev.portenv.recovery-pool.dev-C")
        #expect(try Instance(info: ["PortenvInstance": "dev-C"]).keychainService == "dev.portenv.repository.dev-C")
    }

    /// With the owner at the Mac only (PORTENV_TEST_KEYCHAIN=1): the real
    /// Keychain round trip, in a service of the test's own.
    @Test(.enabled(if: ProcessInfo.processInfo.environment["PORTENV_TEST_KEYCHAIN"] == "1"))
    func theRealKeychainRoundTrips() async throws {
        let store = SystemSecretStore(service: "dev.portenv.test-\(UUID().uuidString)")
        try await onKeychainQueue {
            defer { try? store.delete(account: "a") }
            try store.add(account: "a", data: Data("one".utf8), label: "Portenv test")
            #expect(throws: KeychainStoreError.self) { try store.add(account: "a", data: Data("two".utf8), label: "Portenv test") }
            try store.set(account: "a", data: Data("two".utf8), label: "Portenv test")
            #expect(try store.read(account: "a") == Data("two".utf8))
            try store.delete(account: "a")
            #expect(try store.read(account: "a") == nil)
            try store.delete(account: "a")
        }
    }
}
