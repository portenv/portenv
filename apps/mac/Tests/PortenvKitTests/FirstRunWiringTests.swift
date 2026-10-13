// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security
import Testing

@testable import PortenvKit

/// A store whose calls take a while, as a Keychain prompt does.
final class SlowSecretStore: SecretStore, @unchecked Sendable {
    let inner = FakeSecretStore()
    let delay: TimeInterval
    init(delay: TimeInterval) { self.delay = delay }
    func read(account: String) throws -> Data? { Thread.sleep(forTimeInterval: delay); return try inner.read(account: account) }
    func add(account: String, data: Data, label: String) throws { Thread.sleep(forTimeInterval: delay); try inner.add(account: account, data: data, label: label) }
    func set(account: String, data: Data, label: String) throws { Thread.sleep(forTimeInterval: delay); try inner.set(account: account, data: data, label: label) }
    func delete(account: String) throws { Thread.sleep(forTimeInterval: delay); try inner.delete(account: account) }
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
        #expect(empty.error == BoxMaker.Failure.poolEmpty.errorDescription)
    }

    /// GUIDELINES §3.1: a Keychain call that takes longer than the
    /// threshold says so, and the line goes when it's done.
    @Test func aSlowKeychainSaysItsWaiting() async throws {
        let log = WaitLog()
        let slow = SlowSecretStore(delay: 0.3)
        _ = try await onKeychainQueue(waiting: { log.report($0) }, after: .milliseconds(50)) { try slow.read(account: "x") }
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
