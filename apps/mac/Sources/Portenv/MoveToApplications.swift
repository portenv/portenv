// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// Move to Applications first (PLAN.md 1.5): before the login item is
/// registered, an app outside /Applications (App Translocation included)
/// offers to copy itself there, opens the copy and quits. "Not Now" isn't
/// asked again for the same location.
@MainActor
enum MoveToApplications {
    static var placement: AppPlacement { AppPlacement.of(bundlePath: Bundle.main.bundlePath) }
    private static let declinedKey = "moveDeclinedForPath"

    /// Where the copy goes: /Applications, or a test folder in dev builds
    /// (PORTENV_MOVE_TO), so a UI test never replaces the real app.
    private static var destination: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_MOVE_TO"], !dir.isEmpty {
            return URL(fileURLWithPath: dir, isDirectory: true)
        }
        return URL(fileURLWithPath: "/Applications", isDirectory: true)
    }

    /// Asks when needed. Returns true when the app is quitting because the
    /// copy in Applications is opening.
    static func offerIfNeeded() -> Bool {
        let here = Bundle.main.bundlePath
        guard placement.shouldOfferMove,
              UserDefaults.standard.string(forKey: declinedKey) != here
        else { return false }
        let alert = NSAlert()
        alert.messageText = MoveText.question
        alert.informativeText = MoveText.detail
        alert.addButton(withTitle: MoveText.move)
        alert.addButton(withTitle: MoveText.notNow)
        guard alert.runModal() == .alertFirstButtonReturn else {
            UserDefaults.standard.set(here, forKey: declinedKey)
            return false
        }
        do {
            let copy = try AppMover.copy(Bundle.main.bundleURL, into: destination)
            let config = NSWorkspace.OpenConfiguration()
            config.createsNewApplicationInstance = true
            NSWorkspace.shared.openApplication(at: copy, configuration: config) { _, _ in
                Task { @MainActor in NSApp.terminate(nil) }
            }
            return true
        } catch {
            let failed = NSAlert()
            failed.messageText = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
            failed.runModal()
            return false
        }
    }

    /// `launchctl print` for portenvd's job ("" when it can't be read).
    static func launchdJob() -> String {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = ["print", "gui/\(getuid())/\(LaunchdJob.label)"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        do { try p.run() } catch { return "" }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return String(decoding: data, as: UTF8.self)
    }
}
