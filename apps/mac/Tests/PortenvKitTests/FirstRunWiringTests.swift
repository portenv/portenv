// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security
import Testing

@testable import PortenvKit

/// A store whose writes wait until the test lets them go, as a Keychain
/// prompt left up does.
final class BlockingSecretStore: SecretStore, @unchecked Sendable {
    let inner = FakeSecretStore()
    let entered = DispatchSemaphore(value: 0)
    let release = DispatchSemaphore(value: 0)
    func read(account: String) throws -> Data? { try inner.read(account: account) }
    func add(account: String, data: Data, label: String) throws {
        entered.signal()
        release.wait()
        try inner.add(account: account, data: data, label: label)
    }
    func set(account: String, data: Data, label: String) throws { try inner.set(account: account, data: data, label: label) }
    func delete(account: String) throws { try inner.delete(account: account) }
}

/// Waits up to `seconds` for a signal (a plain wait, outside async code).
func waitFor(_ s: DispatchSemaphore, seconds: Double) -> Bool {
    s.wait(timeout: .now() + seconds) == .success
}

final class LockedBool: @unchecked Sendable {
    private let lock = NSLock()
    private var _value = false
    var value: Bool {
        get { lock.withLock { _value } }
        set { lock.withLock { _value = newValue } }
    }
}

/// What the Keychain wait reported, in order.
final class WaitLog: @unchecked Sendable {
    private let lock = NSLock()
    private var _seen: [Bool] = []
    var seen: [Bool] { lock.withLock { _seen } }
    func report(_ on: Bool) { lock.withLock { _seen.append(on) } }
}

private struct Wired {
    let keys = FakeSecretStore()
    let poolStore = FakeSecretStore()
    let creator: FakeCreator
    let service: KeyedFirstRunService
    let pool: RecoveryPoolStore

    init(boxes: [BoxListing] = []) {
        creator = FakeCreator(keys: keys, boxes: boxes)
        pool = RecoveryPoolStore(store: poolStore)
        let maker = BoxMaker(daemon: creator, repositoryKeys: keys, pool: pool, randomBytes: countingBytes(from: 50), lock: AsyncLock())
        service = KeyedFirstRunService(maker: maker, pool: pool, randomBytes: countingBytes(from: 7))
    }
}

/// First run with the real keys (ADR 0019): the recovery key made on
/// leaving Welcome, the pool filled from it on leaving the recovery key
/// (then the key is dropped), and the first workspace made by BoxMaker.
@MainActor
struct FirstRunWiringTests {
    @Test func theShownKeyFillsThePoolAndMakesTheFirstWorkspace() async throws {
        let w = Wired()
        let flow = FirstRunFlow(service: w.service)
        await flow.next()
        let shown = try #require(flow.recoveryKey)
        let key = try RecoveryKey(parsing: shown)
        #expect(key.grouped == shown)
        #expect(try w.pool.entries().isEmpty) // not before Continue

        flow.recoverySaved = true
        await flow.next()
        #expect(flow.step == .storage)
        let entries = try w.pool.entries()
        #expect(entries.count == RecoveryPool.size)
        for e in entries { #expect(e.password == (try key.password(boxID: e.boxID))) }
        #expect(!w.service.holdsKey) // dropped once the pool is filled

        // Back to the key and forward again: the same key, no second fill.
        flow.back()
        flow.recoverySaved = true
        await flow.next()
        #expect(try w.pool.entries() == entries)
        #expect(flow.recoveryKey == shown)

        await flow.next() // storage: this Mac
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.made == "acme-api")
        let r = try #require(w.creator.requests.first)
        #expect(r.name == "acme-api" && r.boxID == entries[0].boxID && r.storage == "")
        #expect(r.recoveryPassword == Data(entries[0].password.utf8))
        #expect(try w.pool.entries().count == RecoveryPool.size - 1)
    }

    /// R-0149: a fill waiting on a Keychain prompt never holds the
    /// service's lock, so the window can still ask about the key.
    @Test func aBlockedFillNeverBlocksTheService() async throws {
        let store = BlockingSecretStore()
        let creator = FakeCreator(keys: FakeSecretStore())
        let pool = RecoveryPoolStore(store: store)
        let service = KeyedFirstRunService(maker: BoxMaker(daemon: creator, repositoryKeys: FakeSecretStore(), pool: pool, lock: AsyncLock()),
                                           pool: pool, randomBytes: countingBytes(from: 7))
        _ = try await service.makeRecoveryKey()
        let fill = Task { try await service.recoveryKeySaved() }
        await withCheckedContinuation { c in DispatchQueue.global().async { store.entered.wait(); c.resume() } }
        // The write is blocked now. Asking about the key answers at once
        // (on another thread, so a held lock fails the test, not hangs it).
        let answered = DispatchSemaphore(value: 0)
        let held = LockedBool()
        DispatchQueue.global().async { held.value = service.holdsKey; answered.signal() }
        let inTime = waitFor(answered, seconds: 1)
        store.release.signal()
        #expect(inTime, "holdsKey waited for the Keychain write")
        if inTime { #expect(held.value) } // in flight still counts as held
        else { _ = waitFor(answered, seconds: 5) }
        try await fill.value
        #expect(!service.holdsKey)
        #expect(try pool.entries().count == RecoveryPool.size)
    }

    /// Once first run has made the first workspace, the flow lets go of
    /// the key's text (R-0149; best effort).
    @Test func theFlowDropsTheKeysTextWhenDone() async throws {
        let w = Wired()
        let flow = FirstRunFlow(service: w.service)
        await flow.next()
        flow.recoverySaved = true
        await flow.next()
        await flow.next()
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.made == "acme-api")
        #expect(flow.recoveryKey == nil)
    }

    /// A pool that can't be written stays on the recovery key, saying why,
    /// and still holds the key for the next try.
    @Test func aPoolThatCantBeWrittenStaysOnTheKey() async throws {
        let w = Wired()
        w.poolStore.failWrites = [RecoveryPool.account: errSecAuthFailed]
        let flow = FirstRunFlow(service: w.service)
        await flow.next()
        flow.recoverySaved = true
        await flow.next()
        #expect(flow.step == .recoveryKey)
        #expect(flow.error == FirstRunText.poolNotStored)
        #expect(w.service.holdsKey)
        w.poolStore.failWrites = [:]
        await flow.next()
        #expect(flow.step == .storage)
        #expect(try w.pool.entries().count == RecoveryPool.size)
    }

    /// CreateBox's refusal is the step's error, in its own words; an empty
    /// pool says to enter the recovery key.
    @Test func refusalsAreTheFirstWorkspacesError() async throws {
        let w = Wired(boxes: [BoxListing(name: "acme-api", id: "box-eeeeeeeeeeee", creating: false)])
        var key = vectorKey
        try w.pool.fill(from: &key, randomBytes: countingBytes())
        let flow = FirstRunFlow(service: w.service, at: .firstBox)
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .firstBox)
        #expect(flow.error == "A box named acme-api already exists.")

        let empty = FirstRunFlow(service: Wired().service, at: .firstBox)
        empty.boxName = "site"
        await empty.next()
        #expect(empty.needsRecoveryKey)
        #expect(empty.reason == BoxMaker.Failure.poolEmpty.errorDescription)
    }

    /// GUIDELINES §3.1: a Keychain call that takes longer than the
    /// threshold says so, and the line goes when it's done.
    ///
    /// The slow call lasts until the line is shown (up to 10 s), not a
    /// fixed 0.3 s: the line's timer runs on the cooperative pool, one
    /// thread in these tests, which other suites can hold for longer than
    /// that on a loaded machine; the call then ended before the timer ran.
    @Test func aSlowKeychainSaysItsWaiting() async throws {
        let log = WaitLog()
        let shown = DispatchSemaphore(value: 0)
        let inner = FakeSecretStore()
        _ = try await onKeychainQueue(waiting: { on in
            log.report(on)
            if on { shown.signal() }
        }, after: .milliseconds(50)) {
            _ = shown.wait(timeout: .now() + 10)
            return try inner.read(account: "x")
        }
        #expect(log.seen == [true, false])

        let quick = WaitLog()
        _ = try await onKeychainQueue(waiting: { quick.report($0) }, after: .seconds(2)) { 1 }
        #expect(quick.seen == [false])
    }

    /// The service reports Keychain waits to the flow, which shows the line.
    @Test func theFlowShowsTheKeychainLine() async throws {
        let w = Wired()
        let flow = FirstRunFlow(service: w.service)
        w.service.keychainWaiting = { on in Task { @MainActor in flow.keychainWaiting = on } }
        #expect(!flow.keychainWaiting)
        w.service.keychainWaiting?(true)
        try? await Task.sleep(for: .milliseconds(50))
        #expect(flow.waitLine == KeychainWait.line)
        w.service.keychainWaiting?(false)
        try? await Task.sleep(for: .milliseconds(50))
        #expect(flow.waitLine == nil)
    }
}

@MainActor
struct FirstRunRefillTests {
    /// An empty pool asks for the recovery key (the sheet), and once it's
    /// refilled the same Create Workspace goes on.
    @Test func anEmptyPoolOpensTheSheetThenCarriesOn() async throws {
        let keys = FakeSecretStore()
        let creator = FakeCreator(keys: keys)
        let pool = RecoveryPoolStore(store: FakeSecretStore())
        let maker = BoxMaker(daemon: creator, repositoryKeys: keys, pool: pool, randomBytes: countingBytes(from: 50), lock: AsyncLock())
        let flow = FirstRunFlow(service: KeyedFirstRunService(maker: maker, pool: pool), at: .firstBox, fromBoxList: true)
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.needsRecoveryKey)
        #expect(flow.step == .firstBox)
        var key = vectorKey
        try pool.fill(from: &key, randomBytes: countingBytes())
        await flow.recoveryKeyRefilled()
        #expect(!flow.needsRecoveryKey)
        #expect(flow.made == "acme-api")
    }

    /// R-0155's nit: a fill that fails after a newer key replaced the held
    /// one wipes the old key instead of keeping it.
    @Test func aFailedFillAfterANewKeyWipesTheOldOne() async throws {
        let store = BlockingSecretStore()
        let pool = RecoveryPoolStore(store: store)
        let service = KeyedFirstRunService(maker: BoxMaker(daemon: FakeCreator(keys: FakeSecretStore()), repositoryKeys: FakeSecretStore(), pool: pool, lock: AsyncLock()),
                                           pool: pool, randomBytes: countingBytes(from: 7))
        _ = try await service.makeRecoveryKey()
        store.inner.failWrites = [RecoveryPool.account: -25308]
        let fill = Task { try await service.recoveryKeySaved() }
        await withCheckedContinuation { c in DispatchQueue.global().async { store.entered.wait(); c.resume() } }
        _ = try await service.makeRecoveryKey() // a newer key while the write waits
        let before = service.wipedKeys
        store.release.signal()
        await #expect(throws: FirstRunError.self) { try await fill.value }
        #expect(service.wipedKeys == before + 1)
        #expect(service.holdsKey) // the newer key, for the retry
    }
}
