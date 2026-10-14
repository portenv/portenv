// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// The running portenvd: its build, and the file it runs from.
public struct RunningService: Equatable, Sendable {
    public let buildID: String
    /// Its executable, as macOS says (the socket's peer), not as it says.
    public let executable: String?
    public init(buildID: String, executable: String?) {
        self.buildID = buildID
        self.executable = executable
    }
}

/// Asks the running portenvd what it is; nil when it doesn't answer.
public protocol ServiceBuild: Sendable {
    func running() async -> RunningService?
}

/// Restarts this app's own login item.
public protocol ServiceRestarter: Sendable {
    /// Relaunch: portenvd stops, leaving workspaces running, and launchd
    /// starts it again from the bundle, now the updated one (ADR 0014).
    func gentle() async
    /// Unregister and register the login item again.
    func hard() async
}

/// After an in-place update the running portenvd is still the old binary
/// (R-0198): the app compares build IDs (not versions: a lane's or a dev
/// build of the same version differs too) and restarts its own login item,
/// then tries the call again. A portenvd run from another copy of Portenv
/// is never touched. At most one restart at a time.
@MainActor @Observable
public final class ServiceUpdate {
    public nonisolated enum Outcome: Equatable, Sendable {
        case current, restarted
        case refused(String)
    }

    public nonisolated static let updatingText = "Updating Portenv\u{2019}s background service\u{2026}"
    public nonisolated static let reinstall = "Portenv\u{2019}s background service couldn\u{2019}t be updated. Reinstall Portenv: download it again and replace the copy in Applications."

    /// While it restarts: the window and first run say so.
    public private(set) var updating = false

    private let appBuildID: String?
    private let bundlePath: String
    private let service: ServiceBuild
    private let restarter: ServiceRestarter
    private let wait: Duration
    private let poll: Duration
    private let fileExists: @Sendable (String) -> Bool
    private var inFlight: Task<Outcome, Never>?

    public init(appBuildID: String?, bundlePath: String, service: ServiceBuild, restarter: ServiceRestarter,
                wait: Duration = .seconds(10), poll: Duration = .milliseconds(200),
                fileExists: @escaping @Sendable (String) -> Bool = { FileManager.default.fileExists(atPath: $0) }) {
        self.appBuildID = appBuildID
        self.bundlePath = bundlePath
        self.service = service
        self.restarter = restarter
        self.wait = wait
        self.poll = poll
        self.fileExists = fileExists
    }

    /// The app's own build (PortenvBuildID, written by gmake app).
    public static var bundleBuildID: String? { Bundle.main.object(forInfoDictionaryKey: "PortenvBuildID") as? String }

    public func ensureCurrent() async -> Outcome {
        if let inFlight { return await inFlight.value }
        let task = Task { await check() }
        inFlight = task
        defer { inFlight = nil }
        return await task.value
    }

    private func check() async -> Outcome {
        guard let app = appBuildID, !app.isEmpty else { return .current }
        guard let running = await service.running() else { return .current } // not up: launch waits for it
        if running.buildID == app { return .current }
        guard ours(running.executable) else { return .refused(Self.reinstall) }
        updating = true
        defer { updating = false }
        // Each step gets half the wait to come back at this build.
        await restarter.gentle()
        if await cameBack(at: app, within: wait / 2) { return .restarted }
        await restarter.hard()
        if await cameBack(at: app, within: wait / 2) { return .restarted }
        return .refused(Self.reinstall)
    }

    /// Whether the running portenvd is this app's: run from this bundle, or
    /// from a file that's gone (the bundle was replaced in place, as an
    /// update does, and macOS can't name the deleted file). One run from a
    /// copy of Portenv that's still on disk elsewhere is another app's.
    private func ours(_ exe: String?) -> Bool {
        guard let exe else { return true }
        return inside(exe) || !fileExists(exe)
    }

    private func inside(_ exe: String) -> Bool {
        let bundle = URL(fileURLWithPath: bundlePath).standardizedFileURL.resolvingSymlinksInPath().path
        let path = URL(fileURLWithPath: exe).standardizedFileURL.resolvingSymlinksInPath().path
        return path.hasPrefix(bundle + "/")
    }

    private func cameBack(at build: String, within limit: Duration) async -> Bool {
        let deadline = ContinuousClock.now + limit
        while ContinuousClock.now < deadline {
            if await service.running()?.buildID == build { return true }
            try? await Task.sleep(for: poll)
        }
        return false
    }

    /// Runs a call; if portenvd doesn't know it (an older portenvd), brings
    /// up the current one and runs it once more. What can't be fixed says
    /// the reinstall line, never "out of date".
    public func retryingOutOfDate<T: Sendable>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let e as DaemonError where e.message == DaemonError.outOfDate {
            switch await ensureCurrent() {
            case .restarted: return try await body()
            case .current, .refused: throw DaemonError(Self.reinstall)
            }
        }
    }
}
