// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What this build keeps apart from other Portenv builds on the Mac: its
/// launch agent, Keychain service and support folder. A release build has no
/// instance and keeps today's names; a dev lane's build (gmake app LANE=X)
/// carries PortenvInstance=dev-X in its Info.plist, so parallel lanes never
/// share a login item, keys or state. The same rule as core/internal/instance.
public struct Instance: Equatable, Sendable {
    public struct Invalid: Error, Equatable { public let value: String }

    /// nil for a release build, dev-<A–Z> for a dev lane's build.
    public let name: String?

    public init(info: [String: Any]) throws {
        guard let value = info["PortenvInstance"] as? String else {
            name = nil
            return
        }
        guard value.wholeMatch(of: /dev-[A-Z]/) != nil else { throw Invalid(value: value) }
        name = value
    }

    public var daemonLabel: String { suffixed("com.portenv.portenvd", ".") }
    public var daemonPlistName: String { daemonLabel + ".plist" }
    public var keychainService: String { suffixed("dev.portenv.repository", ".") }
    public var folder: String { suffixed("Portenv", "-") }
    /// Set for what the app starts (the CLI, attach), so it uses the same
    /// Keychain service and folder.
    public var environment: [String: String] { name.map { ["PORTENV_INSTANCE": $0] } ?? [:] }

    /// A plain problem when the environment names another instance than
    /// this build's; nil when it agrees or is unset. A release build never
    /// reads the variable (core/internal/instance does the same).
    public func mismatch(environment: [String: String]) -> String? {
        guard let name, let v = environment["PORTENV_INSTANCE"], !v.isEmpty, v != name else { return nil }
        return "This is the \(name) build of Portenv, but PORTENV_INSTANCE is \(v). Unset it, or open the \(v) build."
    }

    private func suffixed(_ base: String, _ sep: String) -> String {
        name.map { base + sep + $0 } ?? base
    }

    /// This build's instance. A bad stamp is a build error, so it stops here
    /// rather than falling back to the release names (and their keys).
    public static let current: Instance = {
        do {
            return try Instance(info: Bundle.main.infoDictionary ?? [:])
        } catch {
            fatalError("Info.plist PortenvInstance is invalid: \(error)")
        }
    }()
}
