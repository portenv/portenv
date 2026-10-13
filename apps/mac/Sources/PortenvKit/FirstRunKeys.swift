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

    /// Whether the recovery key is still held (until the pool is filled).
    public var holdsKey: Bool { lock.withLock { held != nil } }

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

    /// Fills the pool from the held key, once, then wipes the key.
    public func recoveryKeySaved() async throws {
        guard lock.withLock({ !filled && held != nil }) else { return }
        let waits = maker.waits
        do {
            try await onKeychainQueue(waiting: { waits.report($0) }) { [self] in try fillFromHeld() }
        } catch {
            throw FirstRunError(FirstRunText.poolNotStored)
        }
    }

    /// On the Keychain queue: the key leaves `held` only once the pool is
    /// written, and the copy used is the only one left, so wiping it wipes
    /// the key's symbols.
    private func fillFromHeld() throws {
        try lock.withLock {
            guard var key = held else { return }
            held = nil
            do {
                // fill wipes it, whatever happens: keep a copy for a retry.
                var attempt = key
                try pool.fill(from: &attempt)
                key.wipe()
                filled = true
            } catch {
                held = key
                throw error
            }
        }
    }

    public func createBox(_ box: NewBox) async throws {
        _ = try await maker.make(name: box.name)
    }
}
