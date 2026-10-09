// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit

/// The app's one client for portenvd's API, shared by the window, the
/// network watcher and the daemon starter.
enum SharedDaemon {
    static let client: DaemonAPI = (try? DaemonClient()) ?? Unreachable()
}

/// Stands in when the client can't be made at all: every call says portenvd
/// isn't there, so the window shows that instead of hanging.
private struct Unreachable: DaemonAPI {
    private var gone: DaemonError { DaemonError("portenvd can't be reached", unavailable: true) }
    func ping() async -> Bool { false }
    func servers() async throws -> [String] { throw gone }
    func open(_: String) async throws { throw gone }
    func close(_: String) async throws { throw gone }
    func makeSavePoint(_: String) async throws { throw gone }
    func revert(_: String) async throws -> Date? { throw gone }
    func move(_: String, to _: String) async throws { throw gone }
    func check(_: String) async throws -> Bool { throw gone }
    func restart(_: String) async throws { throw gone }
    func state(_: String) async throws -> BoxState { throw gone }
    func watch(_: String) -> AsyncThrowingStream<BoxState, Error> {
        let error = gone
        return AsyncThrowingStream { $0.finish(throwing: error) }
    }
    func keyIDs(_: String) async throws -> [String] { throw gone }
    func provideKeys(_: String, _: [String: Data]) async throws { throw gone }
    func retryPackages(_: String) async throws { throw gone }
    func setNetwork(usable _: Bool) async throws { throw gone }
    func woke() async throws -> Bool { throw gone }
    func relaunch() async throws { throw gone }
    func leaveUnsaved(_: String) async throws { throw gone }
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit] { throw gone }
    func tabs(_: String) async throws -> [TabInfo] { throw gone }
    func newTab(_: String, name _: String) async throws -> TabInfo { throw gone }
    func closeTab(_: String, id _: String) async throws { throw gone }
    func renameTab(_: String, id _: String, name _: String) async throws { throw gone }
    func selectTab(_: String, id _: String) async throws { throw gone }
    func watchTabs(_: String) -> AsyncThrowingStream<[TabInfo], Error> {
        let error = gone
        return AsyncThrowingStream { $0.finish(throwing: error) }
    }
}
