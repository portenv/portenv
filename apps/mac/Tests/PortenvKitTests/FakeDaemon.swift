// SPDX-License-Identifier: Apache-2.0

import Foundation

@testable import PortenvKit

/// Answers like portenvd would. Calls are recorded as ["app", action,
/// arguments…] (the shape the CLI helper had), `state` is what portenvd
/// records (JSON, as `portenv app state` prints it), and the test can push
/// states into `watch`.
final class FakeDaemon: DaemonAPI, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [[String]] = []
    private var _state = #"{"state":"SAVE_STATE_NOT_SAVED_YET"}"#
    private var _provided: [[String: Data]] = []
    private var watchers: [AsyncThrowingStream<BoxState, Error>.Continuation] = []

    var calls: [[String]] { lock.withLock { _calls } }
    var state: String {
        get { lock.withLock { _state } }
        set { lock.withLock { _state = newValue } }
    }
    /// Keys handed over with provide-keys, in order.
    var provided: [[String: Data]] { lock.withLock { _provided } }
    /// Actions that fail with this message, every time.
    var failing: [String: String] = [:]
    /// Actions that fail only the first time.
    var failingOnce: [String: String] = [:]
    /// The time revert reports for the restored save point.
    var revertedTo: Date?
    /// Whether woke reports a box restarted.
    var wokeRestarted = false
    /// Runs before each answer (to look at the app mid-call).
    var onRun: (@Sendable ([String]) async -> Void)?

    private func record(_ args: [String]) async throws {
        lock.withLock { _calls.append(args) }
        await onRun?(args)
        let action = args.count > 1 ? args[1] : ""
        if let message = lock.withLock({ failingOnce.removeValue(forKey: action) }) {
            throw DaemonError(message, unavailable: message.contains("not running"))
        }
        if let message = lock.withLock({ failing[action] }) {
            throw DaemonError(message, unavailable: message.contains("not running"))
        }
    }

    /// Pushes a state (JSON) to every watcher.
    func push(_ json: String) {
        guard let s = BoxState.parse(json) else { return }
        lock.withLock { watchers }.forEach { $0.yield(s) }
    }

    /// Ends every watch stream, as when portenvd goes away.
    func endWatches() {
        let all = lock.withLock { let w = watchers; watchers = []; return w }
        all.forEach { $0.finish(throwing: DaemonError("portenvd is not running", unavailable: true)) }
    }

    func ping() async -> Bool { (try? await record(["app", "ping"])) != nil }
    func servers() async throws -> [String] { try await record(["app", "servers"]); return ["portenv@server-a"] }
    func open(_ box: String) async throws { try await record(["app", "open", box]) }
    func close(_ box: String) async throws { try await record(["app", "close", box]) }
    func makeSavePoint(_ box: String) async throws { try await record(["app", "point", box]) }
    func revert(_ box: String) async throws -> Date? { try await record(["app", "revert", box]); return revertedTo }
    func move(_ box: String, to target: String) async throws { try await record(["app", "move", box, target]) }
    func check(_ box: String) async throws -> Bool { try await record(["app", "check", box]); return true }
    func restart(_ box: String) async throws { try await record(["app", "restart", box]) }
    func state(_ box: String) async throws -> BoxState {
        try await record(["app", "state", box])
        guard let s = BoxState.parse(state) else { throw DaemonError("bad state") }
        return s
    }
    func watch(_ box: String) -> AsyncThrowingStream<BoxState, Error> {
        lock.withLock { _calls.append(["app", "watch", box]) }
        let first = BoxState.parse(state)
        return AsyncThrowingStream { continuation in
            if let failure = lock.withLock({ failing["watch"] }) {
                continuation.finish(throwing: DaemonError(failure, unavailable: true))
                return
            }
            if let first { continuation.yield(first) }
            lock.withLock { watchers.append(continuation) }
        }
    }
    func keyIDs(_ box: String) async throws -> [String] { try await record(["app", "key-ids", box]); return ["box-1", "box-1-storage"] }
    func provideKeys(_ box: String, _ keys: [String: Data]) async throws {
        try await record(["app", "provide-keys", box])
        lock.withLock { _provided.append(keys) }
    }
    func retryPackages(_ box: String) async throws { try await record(["app", "retry-packages", box]) }
    func setNetwork(usable: Bool) async throws { try await record(["app", "network", usable ? "up" : "down"]) }
    func woke() async throws -> Bool { try await record(["app", "woke"]); return wokeRestarted }
    func relaunch() async throws { try await record(["app", "relaunch"]) }
    func leaveUnsaved(_ box: String) async throws { try await record(["app", "leave-unsaved", box]) }
    /// What takeSavedAfterQuit gives out (once).
    var savedAfterQuit: [SavedAfterQuit] = []
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit] {
        try await record(["app", "take-saved-after-quit"])
        return lock.withLock { let s = savedAfterQuit; savedAfterQuit = []; return s }
    }
}
