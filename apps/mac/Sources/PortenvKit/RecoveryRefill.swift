// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What CheckRecoveryKey found.
public struct RecoveryCheckResult: Equatable, Sendable {
    /// Workspaces here that carry a recovery key and could be checked.
    public let checked: Int
    /// The key opened one of them.
    public let opened: Bool
    public init(checked: Int, opened: Bool) {
        self.checked = checked
        self.opened = opened
    }
}

/// portenvd's check of a typed recovery key (ADR 0019).
public protocol RecoveryChecking: Sendable {
    /// Recovery-derived passwords by box ID; never the key itself.
    func checkRecoveryKey(_ passwords: [String: Data]) async throws -> RecoveryCheckResult
}

/// Refills the pool when it runs out (ADR 0019, "When it runs out"): the
/// typed key is checked against the workspaces here that carry a recovery
/// key before anything is written, since a key that passes its own check
/// but isn't the person's would fill the pool with passwords nobody can
/// derive again. With nothing here to check against, it offers a new key
/// instead. Every key is wiped once used. Keychain calls run on the
/// Keychain queue.
public final class RecoveryRefill: @unchecked Sendable {
    public enum Outcome: Equatable, Sendable {
        case refilled
        /// Checked, and it opened none.
        case wrongKey
        /// No workspace here carries a recovery key (or its drive isn't
        /// connected): a typed key can't be checked.
        case nothingToCheck
        /// The key's own check refused it (a typo, a wrong length).
        case invalid(String)
        case failed(String)

        public var message: String? {
            switch self {
            case .refilled: nil
            case .wrongKey: "That\u{2019}s not the recovery key these workspaces were made with. Check it against your copy and try again."
            case .nothingToCheck: "Portenv can\u{2019}t check a recovery key on this Mac: none of its workspaces has one, or the drive they save to isn\u{2019}t connected. Connect the drive and try again, or make a new recovery key."
            case .invalid(let m), .failed(let m): m
            }
        }
    }

    public static let noKey = "There\u{2019}s no new recovery key to save. Make one first."
    /// Why the sheet appeared (R-0164).
    public static let why = "This Mac has used the keys it made from your recovery key. Enter it to make more. Portenv doesn\u{2019}t keep it."
    /// Above a new key made here: the old one still matters (R-0164).
    public static let keepBoth = "Workspaces you already have still open with your old recovery key. Keep both."
    /// The key's own check refused it, before any round trip (R-0164).
    public static let typo = "That doesn\u{2019}t look like a recovery key. Check for a typo."

    /// A pasted or typed key, normalised (spaces, dashes, case) and grouped,
    /// when it's a whole valid key; nil otherwise, so typing isn't fought.
    public static func grouped(_ text: String) -> String? {
        guard var key = try? RecoveryKey(parsing: text) else { return nil }
        defer { key.wipe() }
        return key.grouped
    }

    private let lock = NSLock()
    private var held: RecoveryKey?
    private let daemon: BoxCreating & RecoveryChecking
    private let pool: RecoveryPoolStore
    private let randomBytes: @Sendable (Int) throws -> [UInt8]

    public init(daemon: BoxCreating & RecoveryChecking, pool: RecoveryPoolStore = RecoveryPoolStore(store: SystemSecretStore.recoveryPool),
                randomBytes: @escaping @Sendable (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes) {
        self.daemon = daemon
        self.pool = pool
        self.randomBytes = randomBytes
    }

    /// A new key is held, waiting for "I've saved it".
    public var holdsKey: Bool { lock.withLock { held != nil } }

    /// Checks the typed key, then refills the pool from it.
    public func refill(typed: String) async -> Outcome {
        var key: RecoveryKey
        do {
            key = try RecoveryKey(parsing: typed)
        } catch {
            return .invalid(Self.typo)
        }
        defer { key.wipe() }
        do {
            // Only workspaces that carry a recovery key can be checked, and
            // portenvd refuses more than that (R-0164).
            let ids = try await daemon.listings().filter { !$0.creating && $0.hasRecoveryKey }.map(\.id).prefix(64)
            guard !ids.isEmpty else { return .nothingToCheck }
            var passwords: [String: Data] = [:]
            for id in ids where RecoveryKey.isValidBoxID(id) {
                passwords[id] = Data(try key.password(boxID: id).utf8)
            }
            let check = try await daemon.checkRecoveryKey(passwords)
            guard check.checked > 0 else { return .nothingToCheck }
            guard check.opened else { return .wrongKey }
        } catch {
            return .failed((error as? LocalizedError)?.errorDescription ?? error.localizedDescription)
        }
        return await fill(key)
    }

    /// Make a New Recovery Key: a new key, shown once and held until the
    /// person says they've saved it.
    public func makeNewKey() throws -> String {
        let key = try RecoveryKey.generate(randomBytes: randomBytes)
        lock.withLock {
            held?.wipe()
            held = key
        }
        return key.grouped
    }

    /// The new key is saved: the pool is filled from it, and it's wiped.
    public func newKeySaved() async -> Outcome {
        guard var key = lock.withLock({ () -> RecoveryKey? in defer { held = nil }; return held }) else {
            return .failed(Self.noKey)
        }
        defer { key.wipe() }
        let outcome = await fill(key)
        if outcome != .refilled {
            // Kept for the retry, unless a newer one replaced it.
            lock.withLock { if held == nil { held = key } }
        }
        return outcome
    }

    private func fill(_ key: RecoveryKey) async -> Outcome {
        let pool = self.pool
        do {
            try await onKeychainQueue { [key] in
                var copy = key
                try pool.fill(from: &copy)
            }
            return .refilled
        } catch {
            return .failed(FirstRunText.poolNotStored)
        }
    }
}
