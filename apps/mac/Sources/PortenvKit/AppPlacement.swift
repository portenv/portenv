// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Where the app runs (PLAN.md 1.5, "Move to Applications first"). launchd
/// may refuse to start portenvd from anywhere but /Applications.
public enum AppPlacement: Equatable, Sendable {
    case applications
    /// App Translocation: opened from Downloads or the disk image, macOS
    /// runs a read-only copy from a random path.
    case translocated
    case elsewhere

    /// `applications` is where a move puts the app: /Applications, or a
    /// dev build's test folder (PORTENV_MOVE_TO), so the copy a move opens
    /// never asks again (R-0044).
    public static func of(bundlePath: String, applications: String = "/Applications") -> AppPlacement {
        if bundlePath.contains("/AppTranslocation/") { return .translocated }
        let folder = applications.hasSuffix("/") ? applications : applications + "/"
        return bundlePath.hasPrefix(folder) ? .applications : .elsewhere
    }

    /// Offered as a sheet on the window, alongside the launch (R-0121).
    public var shouldOfferMove: Bool { self != .applications }
}

/// The launch's order (R-0121): the login item is registered and the wait
/// for portenvd starts first. The move question is a sheet on the window,
/// shown alongside and never waited for, so it can't hold up the launch or
/// hide on another Space.
public enum LaunchOrder {
    @MainActor
    public static func begin(offersMove: Bool, register: () async -> Void, offer: () -> Void, wait: () async -> Void) async {
        await register()
        if offersMove { offer() }
        await wait()
    }
}

/// "Move Portenv to Applications?" (PLAN.md 1.5, R-0121).
public enum MoveOffer {
    public enum Answer: Sendable { case move, notNow }

    /// Outside Applications, unless Not Now was chosen for this same place.
    public static func shouldOffer(placement: AppPlacement, declinedPath: String?, here: String) -> Bool {
        placement.shouldOfferMove && declinedPath != here
    }

    /// Carries out the answer. Move: the login item registered from here
    /// is removed first, so launchd never keeps running portenvd from the
    /// old place; then the copy opens (and registers from Applications). A
    /// failed copy registers the login item here again and returns why.
    /// Not Now: remembered for this place.
    @MainActor
    public static func answer(_ answer: Answer,
                              unregister: () async -> Void,
                              register: () async -> Void,
                              copyAndOpen: () async throws -> Void,
                              decline: () -> Void) async -> String? {
        switch answer {
        case .notNow:
            decline()
            return nil
        case .move:
            await unregister()
            do {
                try await copyAndOpen()
                return nil
            } catch {
                await register()
                return (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// The move's and launchd's words (GUIDELINES §10).
public enum MoveText {
    public static let question = "Move Portenv to Applications?"
    public static let detail = "Portenv runs its background service, which keeps your workspaces saved, from the Applications folder. It can copy itself there and open again."
    public static let move = "Move to Applications"
    public static let notNow = "Not Now"
    public static let notPortenv = "There’s already an app called Portenv.app in Applications that isn’t Portenv. Rename or remove it, then try again."
}

public struct MoveError: Error, LocalizedError, Equatable {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Copies the app into Applications. The original stays where it was (a
/// translocated copy can't be removed anyway). An older Portenv is replaced;
/// anything else named Portenv.app never is.
public enum AppMover {
    public static let bundleID = "com.portenv.Portenv"

    /// Moving from inside the destination copies nothing and returns the
    /// app where it is (R-0044). The Portenv that may be replaced is one with
    /// the source's bundle ID, so a dev build (com.portenv.Portenv.dev-X)
    /// replaces an older copy of itself.
    @discardableResult
    public static func copy(_ source: URL, into applications: URL) throws -> URL {
        let fm = FileManager.default
        let target = applications.appendingPathComponent("Portenv.app")
        if same(source, target) { return target }
        if fm.fileExists(atPath: target.path) {
            guard identifier(of: target) == (identifier(of: source) ?? bundleID) else { throw MoveError(MoveText.notPortenv) }
            // Copy beside it first, so a failed copy never leaves no app.
            let staged = applications.appendingPathComponent(".Portenv-\(UUID().uuidString).app")
            try fm.copyItem(at: source, to: staged)
            _ = try fm.replaceItemAt(target, withItemAt: staged)
            return target
        }
        try fm.copyItem(at: source, to: target)
        return target
    }

    static func same(_ a: URL, _ b: URL) -> Bool {
        a.standardizedFileURL.resolvingSymlinksInPath().path == b.standardizedFileURL.resolvingSymlinksInPath().path
    }

    static func identifier(of bundle: URL) -> String? {
        guard let data = try? Data(contentsOf: bundle.appendingPathComponent("Contents/Info.plist")),
              let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
        else { return nil }
        return plist["CFBundleIdentifier"] as? String
    }
}

/// portenvd's launchd job, as `launchctl print gui/<uid>/<label>` shows it.
public enum LaunchdJob {
    /// This build's own job: com.portenv.portenvd, or .dev-X for a dev lane's
    /// build (Instance), never another build's.
    public static func target(uid: uid_t, instance: Instance) -> String {
        "gui/\(uid)/\(instance.daemonLabel)"
    }

    /// The job's last exit code, or nil while it runs or never exited.
    public static func lastExitCode(in printed: String) -> Int? {
        for line in printed.split(separator: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard trimmed.hasPrefix("last exit code = ") else { continue }
            let value = trimmed.dropFirst("last exit code = ".count)
            return Int(value.prefix { $0.isNumber || $0 == "-" })
        }
        return nil
    }

    /// Whether launchd refused to start portenvd (exit 78, EX_CONFIG).
    public static func refused(in printed: String) -> Bool {
        lastExitCode(in: printed) == 78
    }
}

/// The login item's status (SMAppService.Status), without ServiceManagement.
public enum ServiceStatus: Equatable, Sendable {
    case enabled, requiresApproval, notRegistered, notFound
}

public enum ServiceText {
    public static let needsApproval = "Portenv needs permission to run in the background. Turn it on in System Settings \u{203A} General \u{203A} Login Items."
    public static let openLoginItems = "Open Login Items"
    /// launchd refused portenvd after a register (R-0032). SMAppService can
    /// say "enabled" before Portenv is switched on in Login Items, so both
    /// remedies are given, Login Items first.
    public static let notStarted = "Portenv\u{2019}s background service didn\u{2019}t start. Turn Portenv on in System Settings \u{203A} General \u{203A} Login Items, and if it\u{2019}s not in the Applications folder, move it there."
}

/// Why portenvd isn't running, as the app says it (R-0028, R-0032).
/// - launchd refused it (exit 78): both remedies in one line, Login Items
///   first, with Open Login Items and Try Again; the wait ends. SMAppService
///   can't be trusted to tell an approved item apart (it said "enabled"
///   before Portenv was switched on), so the location never stands alone.
/// - Not refused, but the login item isn't enabled: waiting for approval,
///   with Open Login Items; the wait goes on.
/// - Otherwise: no problem yet.
public struct BackgroundServiceProblem: Equatable, Sendable {
    public let message: String
    public let opensLoginItems: Bool
    public let offersTryAgain: Bool
    public init(message: String, opensLoginItems: Bool, offersTryAgain: Bool) {
        self.message = message
        self.opensLoginItems = opensLoginItems
        self.offersTryAgain = offersTryAgain
    }

    /// Whether the wait for portenvd should end (a refusal); approval keeps
    /// waiting.
    public var endsWait: Bool { offersTryAgain }

    public static func of(status: ServiceStatus, launchdJob: String, placement _: AppPlacement) -> BackgroundServiceProblem? {
        if LaunchdJob.refused(in: launchdJob) {
            return BackgroundServiceProblem(message: ServiceText.notStarted, opensLoginItems: true, offersTryAgain: true)
        }
        if status != .enabled {
            return BackgroundServiceProblem(message: ServiceText.needsApproval, opensLoginItems: true, offersTryAgain: false)
        }
        return nil
    }
}
