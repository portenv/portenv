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
/// recovery key is never needed, until the pool runs out.
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

    public init(daemon: BoxCreating, repositoryKeys: SecretStore = SystemSecretStore.repositoryKeys,
                pool: RecoveryPoolStore = RecoveryPoolStore(store: SystemSecretStore.recoveryPool),
                randomBytes: @escaping @Sendable (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes) {
        self.daemon = daemon
        self.repositoryKeys = repositoryKeys
        self.pool = pool
        self.randomBytes = randomBytes
    }

    static func keyLabel(_ boxID: String) -> String { "Portenv repository key (\(boxID))" }

    /// Makes the box `name` on this Mac and returns its box ID. Opening it
    /// (OpenBox, where the first save happens) is the caller's.
    public func make(name: String) async throws -> String {
        // D3's foreign repository: discard, and once more with the next entry.
        do {
            return try await attempt(name: name)
        } catch Failure.refused(let e) where e.kind == .mismatch {
            return try await attempt(name: name)
        }
    }

    private func attempt(name: String) async throws -> String {
        let pool = self.pool
        let boxes = try await daemon.listings()
        let entries = try await onKeychainQueue { try pool.entries() }

        // A1: the entry of this name's unfinished creation, else the first
        // entry no box (finished or not) uses.
        let entry: PoolEntry
        if let half = boxes.first(where: { $0.name == name && $0.creating }),
           let e = entries.first(where: { $0.boxID == half.id }) {
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
            try await settle(e, boxID: entry.boxID)
        }

        // A3: the box exists; its entry goes. If the Keychain says no, the
        // next launch removes it (tidyAtLaunch).
        try? await onKeychainQueue { try pool.remove(boxIDs: [entry.boxID]) }
        return entry.boxID
    }

    /// What a refusal leaves, then the error.
    private func settle(_ e: CreateBoxError, boxID: String) async throws -> Never {
        let keys = repositoryKeys, pool = self.pool
        switch e.kind {
        case .invalid, .unsupported:
            // D1: nothing written. The keys go; the entry stays.
            try? await daemon.discardCreation(boxID: boxID)
            try? await onKeychainQueue { try keys.delete(account: boxID) }
        case .nameTaken, .mismatch:
            // A repository may exist for this box ID: never use it again.
            try? await daemon.discardCreation(boxID: boxID)
            try? await onKeychainQueue { try keys.delete(account: boxID) }
            try? await onKeychainQueue { try pool.remove(boxIDs: [boxID]) }
        case .unreachable:
            // The storage at D1, or portenvd gone mid-way: keep the key (a
            // repository may be encrypted with it) and the entry; the next
            // try reuses both.
            try? await daemon.discardCreation(boxID: boxID)
        case .deadline, .internal, .daemonGone:
            // Resumable: the pending record, the key and the entry stay.
            break
        }
        throw Failure.refused(e)
    }

    private func storedKey(for boxID: String) async throws -> Data {
        let keys = repositoryKeys, randomBytes = self.randomBytes
        return try await onKeychainQueue {
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
        let keys = repositoryKeys, pool = self.pool
        guard let boxes = try? await daemon.listings(),
              let entries = try? await onKeychainQueue({ try pool.entries() }) else { return [] }
        let made = Set(boxes.filter { !$0.creating }.map(\.id))
        try? await onKeychainQueue { try pool.remove(boxIDs: made) }

        var failures: [CreateBoxError] = []
        for half in boxes where half.creating {
            let key = try? await onKeychainQueue { try keys.read(account: half.id) }
            guard let entry = entries.first(where: { $0.boxID == half.id }), let key = key ?? nil else {
                // Without its key or its recovery password it can't be
                // finished; nothing could save into it.
                try? await daemon.discardCreation(boxID: half.id)
                continue
            }
            let request = CreateRequest(name: half.name, boxID: half.id, storage: "", keys: [half.id: key],
                                        recoveryPassword: Data(entry.password.utf8))
            do {
                _ = try await daemon.createBox(request)
                try? await onKeychainQueue { try pool.remove(boxIDs: [half.id]) }
            } catch let e as CreateBoxError {
                failures.append(e)
            } catch {
                failures.append(CreateBoxError(kind: .internal, message: error.localizedDescription))
            }
        }
        return failures
    }
}
