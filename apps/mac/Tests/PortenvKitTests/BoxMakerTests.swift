// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security
import Testing

@testable import PortenvKit

/// portenvd's CreateBox side, as the app sees it: records each request,
/// checks the key was in the Keychain before the call (ADR 0019: keys are
/// stored before anything is encrypted with them), and answers as told.
final class FakeCreator: BoxCreating, @unchecked Sendable {
    private let lock = NSLock()
    private var _requests: [CreateRequest] = []
    private var _discarded: [String] = []
    private var _boxes: [BoxListing] = []
    /// Answers for the next CreateBox calls, in order; then success.
    var answers: [CreateBoxError] = []
    /// The Keychain the app writes to: checked at each CreateBox.
    let keys: FakeSecretStore
    private(set) var keyWasStoredFirst = true

    init(keys: FakeSecretStore, boxes: [BoxListing] = []) {
        self.keys = keys
        _boxes = boxes
    }

    var requests: [CreateRequest] { lock.withLock { _requests } }
    var discarded: [String] { lock.withLock { _discarded } }
    var boxes: [BoxListing] {
        get { lock.withLock { _boxes } }
        set { lock.withLock { _boxes = newValue } }
    }

    func listings() async throws -> [BoxListing] { boxes }

    func createBox(_ r: CreateRequest) async throws -> Bool {
        let stored = (try? keys.read(account: r.boxID)) ?? nil
        return try lock.withLock {
            _requests.append(r)
            if stored != r.keys[r.boxID] { keyWasStoredFirst = false }
            let already = _boxes.contains { $0.id == r.boxID && !$0.creating }
            if !answers.isEmpty {
                let e = answers.removeFirst()
                // Past D1, portenvd has a pending record: the box is creating.
                if ![.invalid, .unreachable, .unsupported, .daemonGone].contains(e.kind),
                   !_boxes.contains(where: { $0.id == r.boxID }) {
                    _boxes.append(BoxListing(name: r.name, id: r.boxID, creating: true))
                }
                throw e
            }
            _boxes.removeAll { $0.id == r.boxID }
            _boxes.append(BoxListing(name: r.name, id: r.boxID, creating: false))
            return already
        }
    }

    func discardCreation(boxID: String) async throws {
        lock.withLock {
            _discarded.append(boxID)
            _boxes.removeAll { $0.id == boxID && $0.creating }
        }
    }
}

private struct Setup {
    let keys = FakeSecretStore()
    let poolStore = FakeSecretStore()
    let creator: FakeCreator
    let maker: BoxMaker
    let pool: RecoveryPoolStore
    let entries: [PoolEntry]

    init(boxes: [BoxListing] = [], fill: Bool = true) throws {
        creator = FakeCreator(keys: keys, boxes: boxes)
        pool = RecoveryPoolStore(store: poolStore)
        if fill { try pool.fill(from: vectorKey, randomBytes: countingBytes()) }
        entries = try pool.entries()
        maker = BoxMaker(daemon: creator, repositoryKeys: keys, pool: pool, randomBytes: countingBytes(from: 50))
    }
}


@Suite struct BoxMakerTests {
    /// A1 to A3: the pool's first entry gives the box ID and the recovery
    /// password; a new repository key is stored (and read back) before
    /// CreateBox; the entry goes only after CreateBox succeeds.
    @Test func makesABoxFromThePoolsFirstEntry() async throws {
        let s = try Setup()
        let id = try await s.maker.make(name: "acme-api")
        let e = s.entries[0]
        #expect(id == e.boxID)
        let r = try #require(s.creator.requests.first)
        #expect(s.creator.requests.count == 1)
        #expect(r.name == "acme-api" && r.boxID == e.boxID && r.storage == "")
        #expect(r.recoveryPassword == Data(e.password.utf8))
        #expect(Array(r.keys.keys) == [e.boxID])
        let key = try #require(r.keys[e.boxID])
        #expect(String(decoding: key, as: UTF8.self).wholeMatch(of: /[0-9a-f]{64}/) != nil)
        #expect(s.creator.keyWasStoredFirst)
        #expect(s.keys.all[e.boxID] == key)
        #expect(s.keys.label(e.boxID) == "Portenv repository key (\(e.boxID))")
        #expect(try s.pool.entries() == Array(s.entries.dropFirst()))
    }

    /// An empty pool asks for the recovery key, and nothing is made.
    @Test func anEmptyPoolAsksForTheRecoveryKey() async throws {
        let s = try Setup(fill: false)
        await #expect(throws: BoxMaker.Failure.poolEmpty) { try await s.maker.make(name: "acme-api") }
        #expect(s.creator.requests.isEmpty)
        #expect(s.keys.all.isEmpty)
        #expect(BoxMaker.Failure.poolEmpty.errorDescription == "Enter your recovery key to keep making workspaces. Portenv doesn't keep it.")
    }

    /// A2: a key that doesn't read back the same is deleted, and CreateBox
    /// never runs.
    @Test func aKeyThatDoesntReadBackStopsBeforeCreateBox() async throws {
        let s = try Setup()
        s.keys.corruptReads = [s.entries[0].boxID]
        await #expect(throws: BoxMaker.Failure.self) { try await s.maker.make(name: "acme-api") }
        #expect(s.creator.requests.isEmpty)
        #expect(s.keys.all.isEmpty)
        #expect(try s.pool.entries() == s.entries)
    }

    /// A2: the Keychain refusing the write: a plain line, nothing made, the
    /// entry kept.
    @Test func aKeychainThatRefusesStopsBeforeCreateBox() async throws {
        let s = try Setup()
        s.keys.failWrites = [s.entries[0].boxID: errSecAuthFailed]
        do {
            _ = try await s.maker.make(name: "acme-api")
            Issue.record("made a box without its key")
        } catch let f as BoxMaker.Failure {
            #expect(f.errorDescription == "The Keychain didn't store the new workspace's key (\(errSecAuthFailed)). Nothing was made.")
        }
        #expect(s.creator.requests.isEmpty)
        #expect(try s.pool.entries() == s.entries)
    }

    /// D1's refusals (nothing written by portenvd): the creation is
    /// discarded, the Keychain items deleted (ADR 0019, D1), and the entry
    /// kept for the next box.
    @Test(arguments: [CreateBoxError.Kind.invalid, .unsupported])
    func aRefusalAtD1DeletesTheKeysAndKeepsTheEntry(kind: CreateBoxError.Kind) async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: kind, message: "No.")]
        await #expect(throws: BoxMaker.Failure.refused(CreateBoxError(kind: kind, message: "No."))) {
            try await s.maker.make(name: "acme-api")
        }
        #expect(s.creator.discarded == [s.entries[0].boxID])
        #expect(s.keys.all.isEmpty)
        #expect(try s.pool.entries() == s.entries)
    }

    /// Unavailable is the storage at D1, or portenvd going away at any
    /// step: the key stays (harmless, and the right one if a repository was
    /// made with it), and so does the entry; the next try reuses both.
    @Test func unavailableKeepsTheKeyAndTheEntry() async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: .unreachable, message: "/Volumes/Drive can't be reached.")]
        await #expect(throws: BoxMaker.Failure.self) { try await s.maker.make(name: "acme-api") }
        let first = try #require(s.creator.requests.first)
        #expect(s.keys.all[first.boxID] == first.keys[first.boxID])
        #expect(try s.pool.entries() == s.entries)
        _ = try await s.maker.make(name: "acme-api")
        #expect(s.creator.requests.last?.keys == first.keys)
    }

    /// A name taken (at D1, or at D6 once the repository exists) or a
    /// repository that doesn't open (D3): the creation is discarded and the
    /// entry is never used again, since a repository may exist for its ID.
    @Test(arguments: [CreateBoxError.Kind.nameTaken, .mismatch])
    func aRefusalThatMayLeaveARepositoryBurnsTheEntry(kind: CreateBoxError.Kind) async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: kind, message: "No."), CreateBoxError(kind: kind, message: "No.")]
        await #expect(throws: BoxMaker.Failure.refused(CreateBoxError(kind: kind, message: "No."))) {
            try await s.maker.make(name: "acme-api")
        }
        #expect(!(try s.pool.entries()).map(\.boxID).contains(s.entries[0].boxID))
        #expect(s.creator.discarded.contains(s.entries[0].boxID))
        #expect(s.keys.all[s.entries[0].boxID] == nil)
    }

    /// D3's foreign repository: the app discards and tries once more with
    /// the pool's next entry (ADR 0019, D3).
    @Test func aForeignRepositoryMovesOnToTheNextEntry() async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: .mismatch, message: "A repository for it already exists.")]
        let id = try await s.maker.make(name: "acme-api")
        #expect(id == s.entries[1].boxID)
        #expect(s.creator.requests.map(\.boxID) == [s.entries[0].boxID, s.entries[1].boxID])
        #expect(try s.pool.entries() == Array(s.entries.dropFirst(2)))
    }

    /// A failure past D1 that a retry resumes (a deadline, an internal
    /// error, portenvd gone): keys, pending record and entry all stay, and
    /// the next try for that name uses the same box ID and the same key.
    @Test(arguments: [CreateBoxError.Kind.deadline, .internal, .daemonGone])
    func aRetryResumesWithTheSameBoxIDAndKey(kind: CreateBoxError.Kind) async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: kind, message: "Later.")]
        await #expect(throws: BoxMaker.Failure.self) { try await s.maker.make(name: "acme-api") }
        let first = try #require(s.creator.requests.first)
        #expect(s.creator.discarded.isEmpty)
        #expect(s.keys.all[first.boxID] == first.keys[first.boxID])
        #expect(try s.pool.entries() == s.entries)

        let id = try await s.maker.make(name: "acme-api")
        let second = try #require(s.creator.requests.last)
        #expect(id == first.boxID)
        #expect(second.keys == first.keys)
        #expect(second.recoveryPassword == first.recoveryPassword)
        #expect(try s.pool.entries() == Array(s.entries.dropFirst()))
    }

    /// Another name never takes the entry of a box still being created.
    @Test func anotherNameSkipsAnEntryBeingCreated() async throws {
        var s = try Setup()
        s = try Setup(boxes: [BoxListing(name: "half-made", id: s.entries[0].boxID, creating: true)])
        let id = try await s.maker.make(name: "acme-api")
        #expect(id == s.entries[1].boxID)
    }

    /// A3: the box is made even if removing its entry fails (the Keychain
    /// said no): launch tidies the pool.
    @Test func aBoxIsMadeEvenIfItsEntryCantBeRemoved() async throws {
        let s = try Setup()
        s.poolStore.failWrites = [RecoveryPool.account: errSecIO]
        let id = try await s.maker.make(name: "acme-api")
        #expect(try s.pool.entries().map(\.boxID).contains(id))
        s.poolStore.failWrites = [:]
        _ = await s.maker.tidyAtLaunch()
        #expect(!(try s.pool.entries()).map(\.boxID).contains(id))
    }

    /// At launch (ADR 0019, A3): every entry whose box exists goes, and a
    /// box left creating is finished with its key and its entry, without
    /// the recovery key.
    @Test func launchRemovesUsedEntriesAndFinishesHalfMadeBoxes() async throws {
        var s = try Setup()
        let done = s.entries[3].boxID, half = s.entries[4].boxID
        s = try Setup(boxes: [BoxListing(name: "acme-web", id: done, creating: false),
                              BoxListing(name: "acme-api", id: half, creating: true)])
        let key = Data(String(repeating: "ab", count: 32).utf8)
        try s.keys.add(account: half, data: key, label: "Portenv repository key (\(half))")
        let failures = await s.maker.tidyAtLaunch()
        #expect(failures.isEmpty)
        let r = try #require(s.creator.requests.first)
        #expect(r.boxID == half && r.name == "acme-api" && r.keys == [half: key])
        #expect(r.recoveryPassword == Data(s.entries[4].password.utf8))
        let left = try s.pool.entries().map(\.boxID)
        #expect(!left.contains(done) && !left.contains(half))
        #expect(left.count == RecoveryPool.size - 2)
    }

    /// A half-made box without its entry or its key can't be finished: it's
    /// discarded (its repository, if any, stays; nothing could save into it).
    @Test func launchDiscardsAHalfMadeBoxItCantFinish() async throws {
        let s = try Setup(boxes: [BoxListing(name: "acme-api", id: "box-ffffffffffff", creating: true)])
        _ = await s.maker.tidyAtLaunch()
        #expect(s.creator.requests.isEmpty)
        #expect(s.creator.discarded == ["box-ffffffffffff"])
    }

    /// No secret in an error, a request's description, or anything printed.
    @Test func noSecretInErrorsOrDescriptions() async throws {
        let s = try Setup()
        s.creator.answers = [CreateBoxError(kind: .internal, message: "Couldn't create the repository.")]
        var errors: [String] = []
        do { _ = try await s.maker.make(name: "acme-api") } catch { errors.append(String(describing: error)); errors.append(error.localizedDescription) }
        let r = try #require(s.creator.requests.first)
        let key = String(decoding: try #require(r.keys[r.boxID]), as: UTF8.self)
        for text in errors + [String(describing: r), String(reflecting: r), "\(r)"] {
            #expect(!text.contains(key))
            #expect(!text.contains(s.entries[0].password))
            #expect(!text.contains(vectorKey.canonical))
        }
    }

    /// portenvd's gRPC codes map to the kinds the app acts on.
    @Test func grpcCodesMapToKinds() {
        #expect(CreateBoxError.Kind(grpcCode: 3) == .invalid)   // InvalidArgument
        #expect(CreateBoxError.Kind(grpcCode: 6) == .nameTaken) // AlreadyExists
        #expect(CreateBoxError.Kind(grpcCode: 9) == .mismatch)  // FailedPrecondition
        #expect(CreateBoxError.Kind(grpcCode: 14) == .unreachable)
        #expect(CreateBoxError.Kind(grpcCode: 12) == .unsupported)
        #expect(CreateBoxError.Kind(grpcCode: 4) == .deadline)
        #expect(CreateBoxError.Kind(grpcCode: 13) == .internal)
        #expect(CreateBoxError.Kind(grpcCode: 2) == .internal)
    }
}
