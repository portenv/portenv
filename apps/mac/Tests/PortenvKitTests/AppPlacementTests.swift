// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Move to Applications first (PLAN.md 1.5): launchd may refuse to start
/// portenvd from anywhere but /Applications (found at the Mac: exit 78 from
/// a temporary folder, and after a rebuild in place).
struct AppPlacementTests {
    @Test func whereTheAppRuns() {
        #expect(AppPlacement.of(bundlePath: "/Applications/Portenv.app") == .applications)
        #expect(AppPlacement.of(bundlePath: "/Users/me/Downloads/Portenv.app") == .elsewhere)
        #expect(AppPlacement.of(bundlePath: "/Users/me/Applications/Portenv.app") == .elsewhere)
        #expect(AppPlacement.of(bundlePath: "/Volumes/Portenv/Portenv.app") == .elsewhere)
        // App Translocation: opened from Downloads or the DMG, macOS runs a
        // read-only copy from a random path.
        #expect(AppPlacement.of(bundlePath: "/private/var/folders/x1/abc/T/AppTranslocation/1234-ABCD/d/Portenv.app") == .translocated)
        // Not fooled by a folder that merely starts with "Applications".
        #expect(AppPlacement.of(bundlePath: "/ApplicationsOld/Portenv.app") == .elsewhere)
        #expect(AppPlacement.of(bundlePath: "/Applications/Utilities/Portenv.app") == .applications)
    }

    /// The question comes before the login item is registered, and only when
    /// the app isn't in /Applications.
    @Test func askOnlyOutsideApplications() {
        #expect(!AppPlacement.applications.shouldOfferMove)
        #expect(AppPlacement.elsewhere.shouldOfferMove)
        #expect(AppPlacement.translocated.shouldOfferMove)
        #expect(MoveText.question == "Move Portenv to Applications?")
    }

    /// Moving copies the app to /Applications/Portenv.app; it never replaces
    /// an app that isn't Portenv, and it says so plainly.
    @Test func movingCopiesIntoApplications() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("portenv-move-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appendingPathComponent("Downloads/Portenv.app")
        let apps = root.appendingPathComponent("Applications")
        try makeBundle(at: source, id: "com.portenv.Portenv", marker: "new")
        try FileManager.default.createDirectory(at: apps, withIntermediateDirectories: true)

        let moved = try AppMover.copy(source, into: apps)
        #expect(moved.path == apps.appendingPathComponent("Portenv.app").path)
        #expect(try String(contentsOf: moved.appendingPathComponent("Contents/marker"), encoding: .utf8) == "new")
        // The original stays where it was (a translocated copy can't be
        // removed anyway).
        #expect(FileManager.default.fileExists(atPath: source.path))

        // An older Portenv in /Applications is replaced.
        try makeBundle(at: source, id: "com.portenv.Portenv", marker: "newer")
        let again = try AppMover.copy(source, into: apps)
        #expect(try String(contentsOf: again.appendingPathComponent("Contents/marker"), encoding: .utf8) == "newer")

        // Something else named Portenv.app is never replaced.
        let other = apps.appendingPathComponent("Portenv.app")
        try FileManager.default.removeItem(at: other)
        try makeBundle(at: other, id: "com.example.other", marker: "other")
        #expect(throws: MoveError(MoveText.notPortenv)) { try AppMover.copy(source, into: apps) }
        #expect(try String(contentsOf: other.appendingPathComponent("Contents/marker"), encoding: .utf8) == "other")
    }

    /// The app it has just moved itself to never asks again (R-0044): the
    /// placement is judged against the move's destination, which in dev
    /// builds is a test folder (PORTENV_MOVE_TO), not /Applications.
    @Test func theDestinationCountsAsApplications() {
        #expect(AppPlacement.of(bundlePath: "/Users/me/portenv-move-test/Portenv.app", applications: "/Users/me/portenv-move-test") == .applications)
        #expect(AppPlacement.of(bundlePath: "/Users/me/portenv-move-test/Portenv.app", applications: "/Users/me/portenv-move-test/") == .applications)
        #expect(AppPlacement.of(bundlePath: "/Users/me/bin/Portenv.app", applications: "/Users/me/portenv-move-test") == .elsewhere)
        #expect(AppPlacement.of(bundlePath: "/Users/me/portenv-move-testing/Portenv.app", applications: "/Users/me/portenv-move-test") == .elsewhere)
        #expect(AppPlacement.of(bundlePath: "/Applications/Portenv.app", applications: "/Users/me/portenv-move-test") == .elsewhere)
    }

    /// Moving from inside the destination never copies the app onto itself:
    /// it carries on (R-0044). A dev build (its own bundle ID, PR #54)
    /// replaces an older copy of itself, not only com.portenv.Portenv.
    @Test func movingOntoItselfCarriesOn() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("portenv-move-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let apps = root.appendingPathComponent("Applications")
        let inPlace = apps.appendingPathComponent("Portenv.app")
        try makeBundle(at: inPlace, id: "com.portenv.Portenv.dev-C", marker: "running")
        let before = try FileManager.default.attributesOfItem(atPath: inPlace.path)[.systemFileNumber] as? Int

        let moved = try AppMover.copy(inPlace, into: apps)
        #expect(moved.standardizedFileURL.path == inPlace.standardizedFileURL.path)
        // Same folder, not a replacement.
        #expect(try FileManager.default.attributesOfItem(atPath: inPlace.path)[.systemFileNumber] as? Int == before)
        #expect(try String(contentsOf: inPlace.appendingPathComponent("Contents/marker"), encoding: .utf8) == "running")
        // A path spelled differently is still the same place.
        #expect(try AppMover.copy(URL(fileURLWithPath: apps.path + "/./Portenv.app"), into: URL(fileURLWithPath: apps.path + "/"))
            .standardizedFileURL.path == inPlace.standardizedFileURL.path)

        let source = root.appendingPathComponent("bin/Portenv.app")
        try makeBundle(at: source, id: "com.portenv.Portenv.dev-C", marker: "newer")
        let replaced = try AppMover.copy(source, into: apps)
        #expect(try String(contentsOf: replaced.appendingPathComponent("Contents/marker"), encoding: .utf8) == "newer")
    }

    private func makeBundle(at url: URL, id: String, marker: String) throws {
        let contents = url.appendingPathComponent("Contents")
        try? FileManager.default.removeItem(at: url)
        try FileManager.default.createDirectory(at: contents, withIntermediateDirectories: true)
        let plist: [String: Any] = ["CFBundleIdentifier": id]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        try data.write(to: contents.appendingPathComponent("Info.plist"))
        try marker.write(to: contents.appendingPathComponent("marker"), atomically: true, encoding: .utf8)
    }
}

/// launchd refusing to start portenvd (exit 78, EX_CONFIG) is said plainly
/// at once, not after the 30-second "isn't running" wait.
struct LaunchdJobTests {
    private let refused = """
    gui/501/com.portenv.portenvd = {
    	active count = 0
    	state = spawn scheduled
    	program identifier = Contents/Helpers/portenvd (mode: 2)
    	runs = 26
    	last exit code = 78: EX_CONFIG
    }
    """
    private let running = """
    gui/501/com.portenv.portenvd = {
    	state = running
    	runs = 1
    	pid = 19842
    	last exit code = (never exited)
    }
    """

    @Test func readsTheLastExitCode() {
        #expect(LaunchdJob.lastExitCode(in: refused) == 78)
        #expect(LaunchdJob.lastExitCode(in: running) == nil)
        #expect(LaunchdJob.lastExitCode(in: "") == nil)
    }

    /// R-0032: SMAppService said "enabled" while launchd still refused,
    /// before Portenv was switched on in Login Items. So a refusal after a
    /// register always gets both remedies in one line, Login Items first,
    /// with Open Login Items and Try Again; it ends the wait.
    @Test func registeredButNotApprovedGetsBothRemedies() {
        for status in [ServiceStatus.enabled, .requiresApproval, .notRegistered, .notFound] {
            for placement in [AppPlacement.applications, .elsewhere, .translocated] {
                let p = BackgroundServiceProblem.of(status: status, launchdJob: refused, placement: placement)
                #expect(p == BackgroundServiceProblem(message: ServiceText.notStarted, opensLoginItems: true, offersTryAgain: true), "\(status) \(placement)")
            }
        }
        #expect(ServiceText.notStarted == "Portenv\u{2019}s background service didn\u{2019}t start. Turn Portenv on in System Settings \u{203A} General \u{203A} Login Items, and if it\u{2019}s not in the Applications folder, move it there.")
    }

    /// Not refused yet, but waiting for approval: said as such, with Open
    /// Login Items; the wait goes on, so the app carries on once it's on.
    @Test func waitingForApprovalSaysSo() {
        for status in [ServiceStatus.requiresApproval, .notRegistered, .notFound] {
            let p = BackgroundServiceProblem.of(status: status, launchdJob: running, placement: .elsewhere)
            #expect(p == BackgroundServiceProblem(message: ServiceText.needsApproval, opensLoginItems: true, offersTryAgain: true, endsWait: false), "\(status)")
            #expect(p?.offersTryAgain == true && p?.endsWait == false, "approval: Try Again, and the wait goes on (R-0250)")
        }
        #expect(ServiceText.needsApproval == "Portenv needs permission to run in the background. Turn it on in System Settings \u{203A} General \u{203A} Login Items.")
    }

    /// Enabled and not refused: no problem yet (the usual wait goes on).
    @Test func enabledAndNotRefusedIsNoProblem() {
        #expect(BackgroundServiceProblem.of(status: .enabled, launchdJob: running, placement: .elsewhere) == nil)
        #expect(BackgroundServiceProblem.of(status: .enabled, launchdJob: "", placement: .applications) == nil)
    }

    /// The app reads its own build's job: a dev lane's build reading the
    /// release job saw another build's exit 78 and said its own running
    /// portenvd "didn't start" (found at the Mac with dev-B, 2026-10-11).
    @Test func theJobReadIsThisBuildsOwn() throws {
        let devB = try Instance(info: ["PortenvInstance": "dev-B"])
        #expect(LaunchdJob.target(uid: 501, instance: devB) == "gui/501/com.portenv.portenvd.dev-B")
        let release = try Instance(info: [:])
        #expect(LaunchdJob.target(uid: 501, instance: release) == "gui/501/com.portenv.portenvd")
    }

    @Test func exit78IsRefused() {
        #expect(LaunchdJob.refused(in: refused))
        #expect(!LaunchdJob.refused(in: running))
        #expect(!LaunchdJob.refused(in: ""))
        // Never a straight apostrophe (GUIDELINES).
        for line in [MoveText.question, MoveText.detail, MoveText.notPortenv, ServiceText.needsApproval, ServiceText.notStarted, ServiceText.openLoginItems] {
            #expect(!line.contains("'"), "straight apostrophe in: \(line)")
        }
    }
}
