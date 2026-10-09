// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the app asks portenvd (its gRPC API on the socket, 1.1). Every box
/// action goes through here; the terminal is still `portenv attach` until
/// 1.2.
public protocol DaemonAPI: Sendable {
    /// Whether portenvd answers.
    func ping() async -> Bool
    /// The servers a box can move to (user@host).
    func servers() async throws -> [String]
    func open(_ box: String) async throws
    func close(_ box: String) async throws
    func makeSavePoint(_ box: String) async throws
    /// Reverts to the last save point and returns its time.
    func revert(_ box: String) async throws -> Date?
    func move(_ box: String, to target: String) async throws
    /// Whether the box agent answers.
    func check(_ box: String) async throws -> Bool
    func restart(_ box: String) async throws
    func state(_ box: String) async throws -> BoxState
    /// The box's state as it changes: at once, then on every change. Ends
    /// (or throws) when portenvd goes away.
    func watch(_ box: String) -> AsyncThrowingStream<BoxState, Error>
    /// The Keychain items the app reads for the box.
    func keyIDs(_ box: String) async throws -> [String]
    /// Keys the app read from the Keychain, held in portenvd's memory only.
    func provideKeys(_ box: String, _ keys: [String: Data]) async throws
    func retryPackages(_ box: String) async throws
    /// The system's network status; `reporter` is this app's process.
    func setNetwork(usable: Bool) async throws
    /// After sleep: check every open box's agent channel. Returns whether a
    /// box was restarted (its channel was gone).
    func woke() async throws -> Bool
    /// An update relaunch: portenvd stops, leaving boxes running.
    func relaunch() async throws
}

/// A failed call to portenvd, with its plain message.
public struct DaemonError: Error, LocalizedError, Equatable {
    public let message: String
    /// portenvd isn't there (not running, or it went away).
    public let unavailable: Bool
    public init(_ message: String, unavailable: Bool = false) {
        self.message = message
        self.unavailable = unavailable
    }
    public var errorDescription: String? { message }
}
