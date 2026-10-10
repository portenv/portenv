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

    public static func of(bundlePath: String) -> AppPlacement {
        if bundlePath.contains("/AppTranslocation/") { return .translocated }
        return bundlePath.hasPrefix("/Applications/") ? .applications : .elsewhere
    }

    /// Asked before the login item is registered.
    public var shouldOfferMove: Bool { self != .applications }
}

/// The move's and launchd's words (GUIDELINES §10).
public enum MoveText {
    public static let question = "Move Portenv to Applications?"
    public static let detail = "Portenv runs its background service, which keeps your boxes saved, from the Applications folder. It can copy itself there and open again."
    public static let move = "Move to Applications"
    public static let notNow = "Not Now"
    public static let notPortenv = "There’s already an app called Portenv.app in Applications that isn’t Portenv. Rename or remove it, then try again."
    public static let launchdRefused = "macOS didn’t start Portenv’s background service, so nothing is being saved. Quit Portenv and open it again; if this lasts, reinstall Portenv."
    public static let launchdRefusedElsewhere = "macOS didn’t start Portenv’s background service from where Portenv is now, so nothing is being saved. Move Portenv to the Applications folder and open it again."
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

    @discardableResult
    public static func copy(_ source: URL, into applications: URL) throws -> URL {
        let fm = FileManager.default
        let target = applications.appendingPathComponent("Portenv.app")
        if fm.fileExists(atPath: target.path) {
            guard identifier(of: target) == bundleID else { throw MoveError(MoveText.notPortenv) }
            // Copy beside it first, so a failed copy never leaves no app.
            let staged = applications.appendingPathComponent(".Portenv-\(UUID().uuidString).app")
            try fm.copyItem(at: source, to: staged)
            _ = try fm.replaceItemAt(target, withItemAt: staged)
            return target
        }
        try fm.copyItem(at: source, to: target)
        return target
    }

    static func identifier(of bundle: URL) -> String? {
        guard let data = try? Data(contentsOf: bundle.appendingPathComponent("Contents/Info.plist")),
              let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
        else { return nil }
        return plist["CFBundleIdentifier"] as? String
    }
}

/// portenvd's launchd job, as `launchctl print gui/<uid>/com.portenv.portenvd`
/// shows it.
public enum LaunchdJob {
    public static let label = "com.portenv.portenvd"

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

    /// The plain line when launchd refused to start portenvd (exit 78,
    /// EX_CONFIG), or nil.
    public static func problem(in printed: String, placement: AppPlacement) -> String? {
        guard lastExitCode(in: printed) == 78 else { return nil }
        return placement == .applications ? MoveText.launchdRefused : MoveText.launchdRefusedElsewhere
    }
}
