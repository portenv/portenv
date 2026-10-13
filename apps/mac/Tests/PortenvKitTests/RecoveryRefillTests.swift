// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// portenvd's CheckRecoveryKey, as the app sees it: the boxes here that
/// carry a recovery key, and the password each one opens with.
final class FakeChecker: BoxCreating, RecoveryChecking, @unchecked Sendable {
    private let lock = NSLock()
    private var _asked: [[String: Data]] = []
    var listed: [BoxListing]
    /// Box ID to the password that opens it (boxes with a recovery key).
    var opensWith: [String: Data]
    var asked: [[String: Data]] { lock.withLock { _asked } }

    init(listed: [BoxListing], opensWith: [String: Data]) {
        self.listed = listed
        self.opensWith = opensWith
    }

    func listings() async throws -> [BoxListing] { listed }
    func createBox(_: CreateRequest) async throws -> Bool { false }
    func discardCreation(boxID _: String) async throws {}

    func checkRecoveryKey(_ passwords: [String: Data]) async throws -> RecoveryCheckResult {
        lock.withLock { _asked.append(passwords) }
        let candidates = passwords.keys.filter { opensWith[$0] != nil }
        return RecoveryCheckResult(checked: candidates.count, opened: candidates.contains { opensWith[$0] == passwords[$0] })
    }
}

private func vectorPW(_ id: String) -> Data { Data((try! vectorKey.password(boxID: id)).utf8) }

@MainActor
struct RecoveryRefillTests {
    private func setup(opensWith: [String: Data], listed: [BoxListing]) -> (RecoveryRefill, FakeChecker, RecoveryPoolStore, FakeSecretStore) {
        let store = FakeSecretStore()
        let pool = RecoveryPoolStore(store: store)
        let checker = FakeChecker(listed: listed, opensWith: opensWith)
        return (RecoveryRefill(daemon: checker, pool: pool, randomBytes: countingBytes(from: 90)), checker, pool, store)
    }

    private let made = [BoxListing(name: "acme-api", id: "box-0123456789ab", creating: false, hasRecoveryKey: true),
                        BoxListing(name: "old", id: "box-0123456789ac", creating: false),
                        BoxListing(name: "half", id: "box-0123456789ad", creating: true, hasRecoveryKey: true)]

    /// The right key, typed as printed: checked first, then the pool is
    /// filled from it. Only derived passwords for finished workspaces that
    /// carry a recovery key are sent (R-0164's bound), never the key.
    @Test func theRightKeyRefillsThePool() async throws {
        let (refill, checker, pool, store) = setup(opensWith: ["box-0123456789ab": vectorPW("box-0123456789ab")], listed: made)
        let outcome = await refill.refill(typed: "k7qf m2xa 9trd hv4p w8lc 3nz5")
        #expect(outcome == .refilled)
        let entries = try pool.entries()
        #expect(entries.count == RecoveryPool.size)
        for e in entries { #expect(e.password == (try vectorKey.password(boxID: e.boxID))) }
        let sent = try #require(checker.asked.first)
        #expect(Array(sent.keys) == ["box-0123456789ab"])
        let text = String(decoding: try #require(store.all[RecoveryPool.account]), as: UTF8.self)
        #expect(!text.contains(vectorKey.canonical))
    }

    /// Another valid key (it passes its own check) is refused, and nothing
    /// is written: its passwords would open nothing later.
    @Test func anotherValidKeyIsRefusedAndNothingIsWritten() async throws {
        let (refill, _, pool, _) = setup(opensWith: ["box-0123456789ab": vectorPW("box-0123456789ab")], listed: made)
        let other = try RecoveryKey.generate(randomBytes: { n in [UInt8](repeating: 7, count: n) })
        #expect(await refill.refill(typed: other.grouped) == .wrongKey)
        #expect(try pool.entries().isEmpty)
        #expect(RecoveryRefill.Outcome.wrongKey.message == "That\u{2019}s not the recovery key these workspaces were made with. Check it against your copy and try again.")
    }

    /// No workspace here carries a recovery key: it can't be checked, so
    /// it isn't taken, and portenvd isn't asked. A recovery-flagged one
    /// whose drive is unplugged comes back as checked 0: the same answer.
    @Test func nothingToCheckAgainstTakesNoKey() async throws {
        let none = made.map { BoxListing(name: $0.name, id: $0.id, creating: $0.creating) }
        let (refill, checker, pool, _) = setup(opensWith: [:], listed: none)
        #expect(await refill.refill(typed: vectorKey.grouped) == .nothingToCheck)
        #expect(checker.asked.isEmpty)
        let (unplugged, _, pool2, _) = setup(opensWith: [:], listed: made)
        #expect(await unplugged.refill(typed: vectorKey.grouped) == .nothingToCheck)
        #expect(try pool.entries().isEmpty && pool2.entries().isEmpty)
    }

    /// The key's check symbol catches a typo here, before any round trip
    /// (R-0164), in one line.
    @Test func aTypoIsCaughtHereAndNothingIsAsked() async throws {
        let (refill, checker, _, _) = setup(opensWith: [:], listed: made)
        for typed in ["K7QR-M2XA-9TRD-HV4P-W8LC-3NZ5", "K7QF-M2XA-9TRD", "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ!"] {
            #expect(await refill.refill(typed: typed) == .invalid(RecoveryRefill.typo))
        }
        #expect(checker.asked.isEmpty)
        #expect(RecoveryRefill.typo == "That doesn\u{2019}t look like a recovery key. Check for a typo.")
    }

    /// A pasted key, with any spaces, dashes and case, shows grouped; a
    /// partly typed one is left alone.
    @Test func aPastedKeyIsShownGrouped() {
        #expect(RecoveryRefill.grouped("k7qf m2xa\t9trd–hv4p w8lc 3nz5") == "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5")
        #expect(RecoveryRefill.grouped("K7QFM2XA9TRDHV4PW8LC3NZ5") == "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5")
        #expect(RecoveryRefill.grouped("K7QF-M2X") == nil)
        #expect(RecoveryRefill.grouped("K7QR-M2XA-9TRD-HV4P-W8LC-3NZ5") == nil) // a typo stays as typed
    }

    /// R-0164's words: why the sheet appeared, and what a new key means for
    /// the old one.
    @Test func theSheetSaysWhyAndWhatHappensToTheOldKey() {
        #expect(RecoveryRefill.why == "This Mac has used the keys it made from your recovery key. Enter it to make more. Portenv doesn\u{2019}t keep it.")
        #expect(RecoveryRefill.keepBoth == "Workspaces you already have still open with your old recovery key. Keep both.")
    }

    /// Make a New Recovery Key: a new key, shown; the pool is filled from it
    /// only once the person says they've saved it; then it's wiped.
    @Test func aNewKeyFillsThePoolOnceSaved() async throws {
        let (refill, _, pool, _) = setup(opensWith: [:], listed: made)
        let shown = try refill.makeNewKey()
        let key = try RecoveryKey(parsing: shown)
        #expect(try pool.entries().isEmpty)
        #expect(await refill.newKeySaved() == .refilled)
        let entries = try pool.entries()
        #expect(entries.count == RecoveryPool.size)
        #expect(entries[0].password == (try key.password(boxID: entries[0].boxID)))
        #expect(!refill.holdsKey)
        #expect(await refill.newKeySaved() == .failed(RecoveryRefill.noKey)) // nothing held twice
    }
}
