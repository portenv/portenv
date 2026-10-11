// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Help › Send Feedback… (R-0088): a new message in the person's mail app,
/// addressed to Portenv, with the app's version, the macOS version and the
/// Mac's chip under a "--" line. Nothing else: no box, name, path, terminal
/// content, user or host name, serial number or other identifier. Facts
/// takes exactly these fields, so nothing else can reach the message.
public enum Feedback {
    /// The one place the address lives.
    public static let address = "contact@everydaymoney.eu"
    public static let subject = "Portenv feedback"
    public static let menuItem = "Send Feedback…"

    /// The alert when no mail app opens mailto: links (GUIDELINES §10).
    public static let noMailApp = "No mail app could open a new message."
    public static let noMailAppDetail = "Write to \(address) from any mail app or webmail."
    public static let copyAddress = "Copy Address"

    /// Info.plist key a release script adds to built copies only; dev
    /// builds have none, and the message then has no commit line.
    public static let commitKey = "PortenvCommit"

    public struct Facts: Equatable, Sendable {
        public var version: String
        public var build: String
        public var commit: String?
        public var macOS: String
        public var chip: String

        public init(version: String, build: String, commit: String?, macOS: String, chip: String) {
            self.version = version
            self.build = build
            self.commit = commit
            self.macOS = macOS
            self.chip = chip
        }

        /// From a bundle's Info.plist, reading only the version, build and
        /// commit keys.
        public init(info: [String: Any], macOS: String, chip: String) {
            func value(_ key: String) -> String? {
                guard let s = info[key] as? String, !s.isEmpty else { return nil }
                return s
            }
            self.init(version: value("CFBundleShortVersionString") ?? "unknown",
                      build: value("CFBundleVersion") ?? "unknown",
                      commit: value(commitKey),
                      macOS: macOS,
                      chip: chip)
        }

        /// This app and this Mac.
        public static func current(bundle: Bundle = .main) -> Facts {
            Facts(info: bundle.infoDictionary ?? [:],
                  macOS: Feedback.macOS(ProcessInfo.processInfo.operatingSystemVersion),
                  chip: Feedback.chip() ?? "unknown")
        }
    }

    /// Two empty lines to write in, then the facts.
    public static func body(_ facts: Facts) -> String {
        var lines = ["", "", "--", "Portenv \(facts.version) (build \(facts.build))"]
        if let commit = facts.commit { lines.append("Commit \(commit)") }
        lines.append("macOS \(facts.macOS)")
        lines.append(facts.chip)
        return lines.joined(separator: "\n") + "\n"
    }

    /// mailto: with the subject and body percent-encoded. URLComponents
    /// leaves "+" alone in a query, which some mail apps read as a space,
    /// so "+", "&", "=" and "?" are encoded here too (RFC 6068).
    public static func mailto(_ facts: Facts) -> URL {
        var c = URLComponents()
        c.scheme = "mailto"
        c.path = address
        c.percentEncodedQueryItems = [
            URLQueryItem(name: "subject", value: encode(subject)),
            URLQueryItem(name: "body", value: encode(body(facts))),
        ]
        // Always valid: a fixed address and a fully encoded query.
        return c.url!
    }

    static func encode(_ s: String) -> String {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "+&=?#")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
    }

    /// "26.0.1", or "26.0" when there's no patch number.
    public static func macOS(_ v: OperatingSystemVersion) -> String {
        v.patchVersion == 0
            ? "\(v.majorVersion).\(v.minorVersion)"
            : "\(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
    }

    /// The chip's name, like "Apple M2" (sysctl machdep.cpu.brand_string).
    public static func chip() -> String? {
        var size = 0
        guard sysctlbyname("machdep.cpu.brand_string", nil, &size, nil, 0) == 0, size > 0 else { return nil }
        var bytes = [CChar](repeating: 0, count: size)
        guard sysctlbyname("machdep.cpu.brand_string", &bytes, &size, nil, 0) == 0 else { return nil }
        let name = String(decoding: bytes.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self)
            .trimmingCharacters(in: .whitespaces)
        return name.isEmpty ? nil : name
    }
}
