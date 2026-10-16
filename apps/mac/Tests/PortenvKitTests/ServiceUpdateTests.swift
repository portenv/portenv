// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A portenvd at some build, run from some path, that a restart may bring
/// back at the app's build (as launchd does after an in-place update).
private final class FakeService: ServiceBuild, ServiceRestarter, @unchecked Sendable {
    private let lock = NSLock()
    var build: String
    var executable: String?
    /// What each restart step brings back: the new build, or nothing.
    var gentleBringsUp: String?
    var hardBringsUp: String?
    private(set) var steps: [String] = []
    /// When set, Relaunch waits until the test lets it go (to look at the
    /// app mid-restart, never racing it).
    var held = false
    func letGo() { lock.withLock { held = false } }
    init(build: String, executable: String?) {
        self.build = build
        self.executable = executable
    }
    func running() async -> RunningService? {
        lock.withLock { RunningService(buildID: build, executable: executable) }
    }
    func gentle() async {
        while lock.withLock({ held }) { try? await Task.sleep(for: .milliseconds(1)) }
        lock.withLock { steps.append("relaunch"); if let b = gentleBringsUp { build = b } }
    }
    func hard() async {
        lock.withLock { steps.append("re-register"); if let b = hardBringsUp { build = b } }
    }
}

@MainActor
struct ServiceUpdateTests {
    let bundle = "/Applications/Portenv.app"
    let inside = "/Applications/Portenv.app/Contents/Helpers/portenvd"

    /// Files on this pretend disk: the other copies of Portenv.
    let disk: Set<String> = ["/Users/me/Downloads/Portenv.app/Contents/Helpers/portenvd", "/Applications/Portenv.app.old/Contents/Helpers/portenvd"]

    private func update(_ s: FakeService, app: String? = "build-2") -> ServiceUpdate {
        let disk = disk
        return ServiceUpdate(appBuildID: app, bundlePath: bundle, service: s, restarter: s, wait: .milliseconds(300), poll: .milliseconds(10),
                             fileExists: { disk.contains($0) })
    }

    /// Another build, from this app's bundle: restarted (Relaunch, which
    /// keeps workspaces running), and up at the app's build.
    @Test func anotherBuildIsRestarted() async {
        let s = FakeService(build: "build-1", executable: inside)
        s.gentleBringsUp = "build-2"
        let u = update(s)
        #expect(await u.ensureCurrent() == .restarted)
        #expect(s.steps == ["relaunch"])
        #expect(!u.updating)
    }

    @Test func theSameBuildIsLeftAlone() async {
        let s = FakeService(build: "build-2", executable: inside)
        #expect(await update(s).ensureCurrent() == .current)
        #expect(s.steps.isEmpty)
    }

    /// A portenvd from another copy of Portenv is never restarted: two
    /// installed copies don't fight over it.
    @Test func aForeignPathIsRefused() async {
        for path in ["/Users/me/Downloads/Portenv.app/Contents/Helpers/portenvd", "/Applications/Portenv.app.old/Contents/Helpers/portenvd"] {
            let s = FakeService(build: "build-1", executable: path)
            #expect(await update(s).ensureCurrent() == .refused(ServiceUpdate.reinstall))
            #expect(s.steps.isEmpty)
        }
    }

    /// Replaced in place (an update): the old portenvd's file is gone, so
    /// macOS names none, or a path that no longer exists. It's this app's.
    @Test func aReplacedBundlesPortenvdIsOurs() async {
        for path in [nil, "/private/var/folders/xy/Portenv.app/Contents/Helpers/portenvd"] {
            let s = FakeService(build: "build-1", executable: path)
            s.gentleBringsUp = "build-2"
            #expect(await update(s).ensureCurrent() == .restarted)
        }
    }

    /// Relaunch doesn't bring the new build: re-registering the login item
    /// is tried; when that doesn't either, the reinstall line.
    @Test func aRestartThatDoesntComeBackSaysReinstall() async {
        let s = FakeService(build: "build-1", executable: inside)
        let u = update(s)
        #expect(await u.ensureCurrent() == .refused(ServiceUpdate.reinstall))
        #expect(s.steps == ["relaunch", "re-register"])
        #expect(!u.updating)
        let t = FakeService(build: "build-1", executable: inside)
        t.hardBringsUp = "build-2"
        #expect(await update(t).ensureCurrent() == .restarted)
    }

    /// "Updating Portenv's background service…" shows while it restarts.
    @Test func itSaysSoWhileItRestarts() async {
        let s = FakeService(build: "build-1", executable: inside)
        let u = update(s)
        s.gentleBringsUp = "build-2"
        s.held = true // the restart waits, so the line is looked at mid-restart (it once finished between looks)
        let task = Task { await u.ensureCurrent() }
        for _ in 0 ..< 2000 where !u.updating { try? await Task.sleep(for: .milliseconds(1)) }
        let seen = u.updating
        s.letGo()
        #expect(await task.value == .restarted)
        #expect(seen)
        #expect(!u.updating)
        #expect(ServiceUpdate.updatingText == "Updating Portenv’s background service…")
    }

    /// An unstamped build (tests, swift run) can't compare: nothing restarts.
    @Test func anUnstampedAppRestartsNothing() async {
        let s = FakeService(build: "build-1", executable: inside)
        #expect(await update(s, app: nil).ensureCurrent() == .current)
        #expect(s.steps.isEmpty)
    }

    /// A call portenvd doesn't know (an older portenvd): restarted, then the
    /// call is tried once more; never "out of date" when that fixes it.
    @Test func anOutOfDateCallRestartsAndRetries() async throws {
        let s = FakeService(build: "build-1", executable: inside)
        s.gentleBringsUp = "build-2"
        let u = update(s)
        var calls = 0
        let result = try await u.retryingOutOfDate {
            calls += 1
            if calls == 1 { throw DaemonError(DaemonError.outOfDate) }
            return "answered"
        }
        #expect(result == "answered" && calls == 2)
        // Not fixable: the reinstall line, not "out of date".
        let f = FakeService(build: "build-1", executable: "/elsewhere/portenvd")
        await #expect(throws: DaemonError(ServiceUpdate.reinstall)) {
            try await update(f).retryingOutOfDate { () throws -> String in throw DaemonError(DaemonError.outOfDate) }
        }
    }
}
