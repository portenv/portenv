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

    @Test func exit78IsSaidPlainly() {
        #expect(LaunchdJob.problem(in: refused, placement: .elsewhere) == MoveText.launchdRefusedElsewhere)
        #expect(LaunchdJob.problem(in: refused, placement: .applications) == MoveText.launchdRefused)
        #expect(LaunchdJob.problem(in: running, placement: .elsewhere) == nil)
        // Never a straight apostrophe (GUIDELINES).
        for line in [MoveText.question, MoveText.detail, MoveText.launchdRefused, MoveText.launchdRefusedElsewhere, MoveText.notPortenv] {
            #expect(!line.contains("'"), "straight apostrophe in: \(line)")
        }
    }
}
