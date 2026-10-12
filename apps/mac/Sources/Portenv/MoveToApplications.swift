// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import ServiceManagement

/// Move to Applications (PLAN.md 1.5): an app outside /Applications (App
/// Translocation included) offers, in a sheet on its window, to copy itself
/// there, open the copy and quit. The launch doesn't wait for the answer
/// (R-0121). "Not Now" isn't asked again for the same location.
@MainActor
enum MoveToApplications {
    static var placement: AppPlacement {
        AppPlacement.of(bundlePath: Bundle.main.bundlePath, applications: destination.path)
    }
    private static let declinedKey = "moveDeclinedForPath"

    /// Where the copy goes: /Applications, or a test folder in dev builds
    /// (PORTENV_MOVE_TO), so a UI test never replaces the real app.
    private static var destination: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_MOVE_TO"], !dir.isEmpty {
            return URL(fileURLWithPath: dir, isDirectory: true)
        }
        return URL(fileURLWithPath: "/Applications", isDirectory: true)
    }

    /// Whether to offer the move: outside Applications, unless Not Now was
    /// chosen for this same place.
    static var shouldOffer: Bool {
        MoveOffer.shouldOffer(placement: placement,
                              declinedPath: UserDefaults.standard.string(forKey: declinedKey),
                              here: Bundle.main.bundlePath)
    }

    /// Carries out the sheet's answer (R-0121). Move removes the login item
    /// registered from here, copies the app, opens the copy and quits; a
    /// failed move registers again and returns why.
    static func answer(_ answer: MoveOffer.Answer) async -> String? {
        await MoveOffer.answer(
            answer,
            unregister: { try? await LoginItem.service.unregister() },
            register: { _ = await LoginItem.ensureRegistered() },
            copyAndOpen: {
                let copy = try AppMover.copy(Bundle.main.bundleURL, into: destination)
                let config = NSWorkspace.OpenConfiguration()
                config.createsNewApplicationInstance = true
                _ = try await NSWorkspace.shared.openApplication(at: copy, configuration: config)
                NSApp.terminate(nil)
            },
            decline: { UserDefaults.standard.set(Bundle.main.bundlePath, forKey: declinedKey) }
        )
    }

    /// The login item's status, read from ServiceManagement.
    static var serviceStatus: ServiceStatus {
        switch LoginItem.service.status {
        case .enabled: .enabled
        case .requiresApproval: .requiresApproval
        case .notRegistered: .notRegistered
        default: .notFound
        }
    }

    /// `launchctl print` for portenvd's job ("" when it can't be read).
    static func launchdJob() -> String {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = ["print", LaunchdJob.target(uid: getuid(), instance: Instance.current)]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        do { try p.run() } catch { return "" }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return String(decoding: data, as: UTF8.self)
    }
}
