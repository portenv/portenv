// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the app asks portenvd (its gRPC API on the socket, 1.1). Every box
/// action goes through here; the terminal is still `portenv attach`.
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
    /// The box's saves (its history), for the inspector. A box open on a
    /// server lists its saves there, not through this portenvd.
    func saves(_ box: String) async throws -> [SaveInfo]
    /// The system's network status; `reporter` is this app's process.
    func setNetwork(usable: Bool) async throws
    /// After sleep: check every open box's agent channel. Returns whether a
    /// box was restarted (its channel was gone).
    func woke() async throws -> Bool
    /// An update relaunch: portenvd stops, leaving boxes running.
    func relaunch() async throws
    /// Quit Anyway: portenvd records the quit marker and lets go of the
    /// box unsaved; the next open saves it first thing.
    func leaveUnsaved(_ box: String) async throws
    /// Boxes saved in the background after Quit Anyway since last asked
    /// (each given out once).
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit]
    /// The box's tabs (its terminal's tmux windows), in order (1.2).
    func tabs(_ box: String) async throws -> [TabInfo]
    /// A new tab at the end, shown in the terminal.
    func newTab(_ box: String, name: String) async throws -> TabInfo
    func closeTab(_ box: String, id: String) async throws
    func renameTab(_ box: String, id: String, name: String) async throws
    func selectTab(_ box: String, id: String) async throws
    /// The tabs at once, then on every change. Ends (or throws) when the box
    /// closes, its agent goes away, or portenvd does.
    func watchTabs(_ box: String) -> AsyncThrowingStream<[TabInfo], Error>
}

/// A box saved in the background after Quit Anyway.
public struct SavedAfterQuit: Equatable, Sendable {
    public let box: String
    public let savedAt: Date
    public init(box: String, savedAt: Date) {
        self.box = box
        self.savedAt = savedAt
    }
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

    /// portenvd is older than the app: it doesn't know a call the app makes.
    public static let outOfDate =
        "Portenv's background service is out of date. Quit Portenv and open it again; if this keeps happening, reinstall Portenv."

    /// A gRPC failure as the app shows it: portenvd's own plain message,
    /// except a call portenvd doesn't have (an older portenvd), which says
    /// what to do instead of gRPC's "unknown method" (§10).
    public static func fromRPC(message: String, unimplemented: Bool, unavailable: Bool) -> DaemonError {
        unimplemented ? DaemonError(outOfDate) : DaemonError(message, unavailable: unavailable)
    }
}
