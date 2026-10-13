// SPDX-License-Identifier: Apache-2.0

import Foundation

/// First run with real keys (ADR 0019): the recovery key is made on leaving
/// Welcome and held here, in memory only; on leaving the recovery key step
/// it fills the pool and is wiped; the first workspace is made by BoxMaker
/// (steps A1 to A3 around CreateBox). The flow only ever sees the key's
/// text, to show, print and copy (best effort: SwiftUI's copies aren't
/// ours to clear).
public final class KeyedFirstRunService: FirstRunService, @unchecked Sendable {
    private let lock = NSLock()
    private var held: RecoveryKey?
    private var filled = false
    /// A fill is writing the pool (with the lock released).
    private var filling = false
    private let maker: BoxMaker
    private let pool: RecoveryPoolStore
    private let randomBytes: @Sendable (Int) throws -> [UInt8]

    public init(maker: BoxMaker, pool: RecoveryPoolStore = RecoveryPoolStore(store: SystemSecretStore.recoveryPool),
                randomBytes: @escaping @Sendable (Int) throws -> [UInt8] = RecoveryKey.systemRandomBytes) {
        self.maker = maker
        self.pool = pool
        self.randomBytes = randomBytes
    }

    /// Hears Keychain calls that wait, for the window's line.
    public var keychainWaiting: (@Sendable (Bool) -> Void)? {
        get { maker.waits.handler }
        set { maker.waits.handler = newValue }
    }

    /// Whether the recovery key is still held (until the pool is filled;
    /// a fill in flight still counts). Never waits for the Keychain.
    public var holdsKey: Bool { lock.withLock { held != nil || filling } }

    public func makeRecoveryKey() async throws -> String {
        let key = try RecoveryKey.generate(randomBytes: randomBytes)
        let text = key.grouped
        lock.withLock {
            held?.wipe()
            held = key
            filled = false
        }
        return text
    }

    /// Fills the pool from the held key, once, then wipes the key. The lock
    /// is never held across the Keychain write, which can wait behind a
    /// prompt for as long as the person leaves it (R-0149): the key is taken
    /// out under the lock, written without it, then put back on failure.
    public func recoveryKeySaved() async throws {
        let taken: RecoveryKey? = lock.withLock {
            guard !filled, !filling, let key = held else { return nil }
            held = nil
            filling = true
            return key
        }
        guard var key = taken else { return }
        let pool = self.pool, waits = maker.waits
        do {
            // The closure takes its own copy, released when it ends, so the
            // wipe below reaches the key's only buffer.
            try await onKeychainQueue(waiting: { waits.report($0) }) { [key] in
                // fill wipes this copy, whatever happens.
                var attempt = key
                try pool.fill(from: &attempt)
            }
        } catch {
            lock.withLock {
                filling = false
                if held == nil { held = key } // kept for the retry
            }
            throw FirstRunError(FirstRunText.poolNotStored)
        }
        lock.withLock {
            filling = false
            filled = true
        }
        key.wipe()
    }

    public func createBox(_ box: NewBox) async throws {
        _ = try await maker.make(name: box.name)
    }
}
