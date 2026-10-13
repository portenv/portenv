// SPDX-License-Identifier: Apache-2.0

import Foundation

/// A box as ListBoxes reports it, with whether CreateBox hasn't finished it.
public struct BoxListing: Equatable, Sendable {
    public let name: String
    public let id: String
    public let creating: Bool
    public init(name: String, id: String, creating: Bool) {
        self.name = name
        self.id = id
        self.creating = creating
    }
}

/// What the app hands portenvd's CreateBox (ADR 0019). It never prints its
/// keys or its recovery password.
public struct CreateRequest: Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let name: String
    public let boxID: String
    /// "" for this Mac only (the only kind CreateBox makes in 1.5).
    public let storage: String
    public let keys: [String: Data]
    public let recoveryPassword: Data

    public var description: String { "[create \(name) as \(boxID)]" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: ["name": name, "boxID": boxID, "storage": storage], displayStyle: .struct) }
}

/// CreateBox's refusal, by what the app does about it.
public struct CreateBoxError: Error, LocalizedError, Equatable {
    public enum Kind: Equatable, Sendable {
        case invalid      // InvalidArgument (D1)
        case nameTaken    // AlreadyExists (D1, or D6 once the repository exists)
        case mismatch     // FailedPrecondition (D1's box ID, or D3's foreign repository)
        case unreachable  // Unavailable: the storage at D1, or portenvd going away
        case unsupported  // Unimplemented: storage CreateBox can't make yet, or an older portenvd
        case deadline     // DeadlineExceeded: restic, or the wait for another creation
        case `internal`   // anything else
        case daemonGone   // no answer at all

        /// From a gRPC status code.
        public init(grpcCode: Int) {
            switch grpcCode {
            case 3: self = .invalid
            case 6: self = .nameTaken
            case 9: self = .mismatch
            case 14: self = .unreachable
            case 12: self = .unsupported
            case 4: self = .deadline
            default: self = .internal
            }
        }
    }

    public let kind: Kind
    /// portenvd's one plain line, never holding a key.
    public let message: String
    public init(kind: Kind, message: String) {
        self.kind = kind
        self.message = message
    }
    public var errorDescription: String? { message }
}

/// One holder at a time, across suspension points (an actor alone lets
/// calls interleave at every await). Waiters go in order.
public final class AsyncLock: @unchecked Sendable {
    private let lock = NSLock()
    private var held = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    /// Runs when a caller has to wait (for tests).
    private let onWait: (@Sendable () -> Void)?

    public init(onWait: (@Sendable () -> Void)? = nil) { self.onWait = onWait }

    /// The app's one lock for making workspaces.
    public static let creating = AsyncLock()

    public func withLock<T: Sendable>(_ body: () async throws -> T) async rethrows -> T {
        await acquire()
        defer { release() }
        return try await body()
    }

    private func acquire() async {
        let free: Bool = lock.withLock {
            if held { return false }
            held = true
            return true
        }
        if free { return }
        onWait?()
        await withCheckedContinuation { c in
            let now: Bool = lock.withLock {
                if !held { held = true; return true }
                waiters.append(c)
                return false
            }
            if now { c.resume() }
        }
    }

    private func release() {
        let next: CheckedContinuation<Void, Never>? = lock.withLock {
            if waiters.isEmpty { held = false; return nil }
            return waiters.removeFirst() // held passes to it
        }
        next?.resume()
    }
}

/// portenvd's calls for making boxes.
public protocol BoxCreating: Sendable {
    func listings() async throws -> [BoxListing]
    /// True when an earlier call had finished it (already_created).
    func createBox(_ request: CreateRequest) async throws -> Bool
    func discardCreation(boxID: String) async throws
}

/// Makes a box from the app (ADR 0019, steps A1 to A3 around portenvd's D1
/// to D6): the pool's next entry gives the box ID and the recovery
/// password; the repository key is made, stored in the Keychain and read
/// back before CreateBox; the entry goes only once the box exists. The
/// recovery key is never needed, until the pool runs out. `make` and
/// `tidyAtLaunch` run one at a time (with the pool's read, change and
/// write inside), so two never take the same entry.
public struct BoxMaker: Sendable {
    public enum Failure: Error, LocalizedError, Equatable {
        /// The pool is empty: New Box asks for the recovery key once.
        case poolEmpty
        /// The Keychain didn't store the key (nothing was made).
        case keychain(OSStatus)
        /// The key read back from the Keychain wasn't the one written.
        case keyNotStored
        case refused(CreateBoxError)

        public var errorDescription: String? {
            switch self {
            case .poolEmpty: "Enter your recovery key to keep making workspaces. Portenv doesn't keep it."
            case .keychain(let status): "The Keychain didn't store the new workspace's key (\(status)). Nothing was made."
            case .keyNotStored: "Portenv couldn't store the new workspace's key in the Keychain. Nothing was made."
            case .refused(let e): e.message
            }
        }
    }

    let daemon: BoxCreating
    let repositoryKeys: SecretStore
    let pool: RecoveryPoolStore
    let randomBytes: @Sendable (Int) throws -> [UInt8]
    let lock: AsyncLock
    /// Hears a Keychain call that's waiting (a prompt), to say so.
    public let waits: KeychainWaitRelay

    public init(daemon: BoxCreating, repositoryKeys: SecretStore = SystemSecretStore.repositoryKeys,
                pool: RecoveryPoolStore = RecoveryPoolStore(store: SystemSecretStore.recoveryPool),
                randomBytes: @escaping @Sendable (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes,
                lock: AsyncLock = .creating, waits: KeychainWaitRelay = KeychainWaitRelay()) {
        self.daemon = daemon
        self.repositoryKeys = repositoryKeys
        self.pool = pool
        self.randomBytes = randomBytes
        self.lock = lock
        self.waits = waits
    }

    /// A Keychain call, said when it waits (GUIDELINES §3.1).
    private func keychain<T: Sendable>(_ body: @escaping @Sendable () throws -> T) async throws -> T {
        let waits = self.waits
        return try await onKeychainQueue(waiting: { waits.report($0) }, body)
    }

    static func keyLabel(_ boxID: String) -> String { "Portenv repository key (\(boxID))" }

    /// Makes the box `name` on this Mac and returns its box ID. Opening it
    /// (OpenBox, where the first save happens) is the caller's.
    public func make(name: String) async throws -> String {
        try await lock.withLock {
            // D3's foreign repository: discard, and once more with the next entry.
            do {
                return try await attempt(name: name)
            } catch Failure.refused(let e) where e.kind == .mismatch {
                return try await attempt(name: name)
            }
        }
    }

    private func attempt(name: String) async throws -> String {
        let pool = self.pool
        var boxes = try await daemon.listings()
        let entries = try await keychain { try pool.entries() }

        // A1: the entry of this name's unfinished creation, else the first
        // entry no box (finished or not) uses.
        let entry: PoolEntry
        let half = boxes.first(where: { $0.name == name && $0.creating })
        if let half, let e = entries.first(where: { $0.boxID == half.id }) {
            entry = e
        } else if let half {
            // This name's creation lost its entry (the pool was refilled):
            // it can't be finished. Discard it first, or the next entry
            // would be refused as a taken name, try after try.
            try await daemon.discardCreation(boxID: half.id)
            await deleteKeyUnlessMade(half.id)
            boxes.removeAll { $0.id == half.id }
            guard let e = entries.first(where: { e in !boxes.contains { $0.id == e.boxID } }) else { throw Failure.poolEmpty }
            entry = e
        } else if let e = entries.first(where: { e in !boxes.contains { $0.id == e.boxID } }) {
            entry = e
        } else {
            throw Failure.poolEmpty
        }

        // A2: the repository key, stored and read back. One already stored
        // under this box ID (an earlier try) is the one to use: a
        // repository may already be encrypted with it.
        let key = try await storedKey(for: entry.boxID)
        let request = CreateRequest(name: name, boxID: entry.boxID, storage: "", keys: [entry.boxID: key],
                                    recoveryPassword: Data(entry.password.utf8))
        do {
            _ = try await daemon.createBox(request)
        } catch let e as CreateBoxError {
            await settle(e, boxID: entry.boxID)
            throw Failure.refused(e)
        }

        // A3: the box exists; its entry goes. If the Keychain says no, the
        // next launch removes it (tidyAtLaunch).
        try? await keychain { try pool.remove(boxIDs: [entry.boxID]) }
        return entry.boxID
    }

    /// What a refusal leaves (ADR 0019; R-0122). A repository key is never
    /// deleted while a finished workspace uses its box ID.
    private func settle(_ e: CreateBoxError, boxID: String) async {
        let pool = self.pool
        switch e.kind {
        case .invalid, .unsupported:
            // D1: nothing written. The key goes; the entry stays.
            try? await daemon.discardCreation(boxID: boxID)
            await deleteKeyUnlessMade(boxID)
        case .nameTaken:
            // At D1 nothing was written: the key goes, the entry stays. At
            // D6 the repository exists with this box ID (its pending record
            // shows it creating): never use the entry again.
            let atD6 = (try? await daemon.listings())?.contains { $0.id == boxID && $0.creating } ?? false
            if atD6 {
                try? await daemon.discardCreation(boxID: boxID)
                try? await keychain { try pool.remove(boxIDs: [boxID]) }
            }
            await deleteKeyUnlessMade(boxID)
        case .mismatch:
            // The box ID is in use, or a repository that doesn't open is at
            // its path: never use the entry again.
            if !(await isMade(boxID)) { try? await daemon.discardCreation(boxID: boxID) }
            await deleteKeyUnlessMade(boxID)
            try? await keychain { try pool.remove(boxIDs: [boxID]) }
        case .unreachable:
            // The storage at D1, or portenvd gone at any step: the key (a
            // repository may be encrypted with it), the entry and any
            // pending record stay; the next try reuses them.
            break
        case .deadline, .internal, .daemonGone:
            // Resumable: the pending record, the key and the entry stay.
            break
        }
    }

    /// Whether a finished workspace has this box ID, asked right now. When
    /// portenvd doesn't answer, it counts as made: nothing is deleted.
    private func isMade(_ boxID: String) async -> Bool {
        guard let boxes = try? await daemon.listings() else { return true }
        return boxes.contains { $0.id == boxID && !$0.creating }
    }

    /// Deletes the repository key for boxID, unless a finished workspace
    /// uses that box ID (checked right before deleting).
    private func deleteKeyUnlessMade(_ boxID: String) async {
        guard !(await isMade(boxID)) else { return }
        let keys = repositoryKeys
        try? await keychain { try keys.delete(account: boxID) }
    }

    private func storedKey(for boxID: String) async throws -> Data {
        let keys = repositoryKeys, randomBytes = self.randomBytes
        return try await keychain {
            if let existing = try keys.read(account: boxID) { return existing }
            // 32 random bytes as hex, like keys.NewKey.
            var raw = try randomBytes(32)
            defer { for i in raw.indices { raw[i] = 0 } }
            guard raw.count == 32 else { throw RecoveryKey.GenerateError(message: "This Mac couldn't make random numbers for the new workspace's key.") }
            let key = Data(raw.map { String(format: "%02x", $0) }.joined().utf8)
            do {
                try keys.add(account: boxID, data: key, label: Self.keyLabel(boxID))
            } catch let e as KeychainStoreError {
                throw Failure.keychain(e.status)
            }
            guard try keys.read(account: boxID) == key else {
                try? keys.delete(account: boxID)
                throw Failure.keyNotStored
            }
            return key
        }
    }

    /// At launch (ADR 0019, A3): removes every entry whose box exists, and
    /// finishes each box left creating with its key and its entry, or
    /// discards it when either is gone. Returns what couldn't be finished.
    public func tidyAtLaunch() async -> [CreateBoxError] {
        await lock.withLock { await tidy() }
    }

    private func tidy() async -> [CreateBoxError] {
        let keys = repositoryKeys, pool = self.pool
        guard let boxes = try? await daemon.listings(),
              let entries = try? await keychain({ try pool.entries() }) else { return [] }
        let made = Set(boxes.filter { !$0.creating }.map(\.id))
        try? await keychain { try pool.remove(boxIDs: made) }

        var failures: [CreateBoxError] = []
        for half in boxes where half.creating {
            guard let entry = entries.first(where: { $0.boxID == half.id }) else {
                // Without its recovery password it can't be finished, and
                // nothing could save into it: discard it, and its orphaned key.
                try? await daemon.discardCreation(boxID: half.id)
                await deleteKeyUnlessMade(half.id)
                continue
            }
            let key: Data?
            do {
                key = try await keychain { try keys.read(account: half.id) }
            } catch {
                // The Keychain said no (a denied prompt): not "no key". Next launch.
                continue
            }
            guard let key else {
                // Its key is gone: a repository may be encrypted with the
                // lost key, so the entry can't serve another workspace.
                try? await daemon.discardCreation(boxID: half.id)
                try? await keychain { try pool.remove(boxIDs: [half.id]) }
                continue
            }
            let request = CreateRequest(name: half.name, boxID: half.id, storage: "", keys: [half.id: key],
                                        recoveryPassword: Data(entry.password.utf8))
            do {
                _ = try await daemon.createBox(request)
                try? await keychain { try pool.remove(boxIDs: [half.id]) }
            } catch let e as CreateBoxError {
                await settle(e, boxID: half.id)
                failures.append(e)
            } catch {
                failures.append(CreateBoxError(kind: .internal, message: error.localizedDescription))
            }
        }
        return failures
    }
}
