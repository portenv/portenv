// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security

/// Portenv › Settings… (⌘,): a toolbar window with five panes, in the
/// settings board's order (R-0137).
public enum SettingsPane: String, CaseIterable, Sendable {
    case general, servers, agents, recoveryKey, updates

    public var title: String {
        switch self {
        case .general: "General"
        case .servers: "Servers"
        case .agents: "Agents"
        case .recoveryKey: "Recovery Key"
        case .updates: "Updates"
        }
    }

    /// The toolbar item's SF Symbol.
    public var symbol: String {
        switch self {
        case .general: "gearshape"
        case .servers: "server.rack"
        case .agents: "person.2"
        case .recoveryKey: "key"
        case .updates: "arrow.clockwise"
        }
    }
}

/// The settings board's copy. Each row is a label, its state, and the line
/// under it that says what the setting does.
public enum SettingsText {
    // General (not drawn on the board: appearance and the terminal font).
    public static let appearance = "Appearance"
    public static let appearanceState = "Follows your Mac"
    public static let appearanceLine = "Light or dark, as set in System Settings › Appearance."
    public static let terminalFont = "Terminal font"
    public static let terminalFontState = "SF Mono, 13 pt"

    // Servers.
    public static let yourServers = "Your servers"
    public static let noServers = "No servers yet."

    // Agents.
    public static let agentsWithAccess = "Agents with access now"
    public static let noAgents = "No agents have access."

    // Recovery Key.
    public static let recoveryHeading = "Recovery key"
    public static let recoveryLine = "Opens your workspaces\u{2019} saves if this Mac\u{2019}s keys are ever lost."
    public static let noRecoveryKey = "Not made on this Mac"
    public static let noRecoveryKeyLine = "Portenv makes it when you make your first workspace in the app."
    public static let recoveryUnreadable = "Couldn\u{2019}t check"
    public static let recoveryUnreadableLine = "The Keychain didn\u{2019}t answer. Open Settings again to try once more."
    public static let whereSavesGo = "Where saves go"
    public static let thisMacOnly = "This Mac only"
    public static let thisMacOnlyLine = "Saves have no copy off this Mac yet."
    public static let encryptedNote = "Saves are encrypted before they\u{2019}re stored, wherever that is."

    // Updates.
    public static let updatesHeading = "Updates"
    public static func version(_ facts: Feedback.Facts) -> String { "Version \(facts.version) (\(facts.build))" }
    /// Until Sparkle (1.8): no update check runs, so no switch, no Check
    /// Now and no "Up to date" (R-0149).
    public static let downloadForNow = "New versions come as a download for now. Portenv will check by itself in a later build."
    public static let privacyHeading = "Privacy"
    public static let whatPortenvSends = "What Portenv sends"
    /// What Portenv sends, from the real counting setting (R-0149): nothing
    /// about the person or their work, and usage numbers only if they chose
    /// to be counted.
    public static func whatPortenvSendsLine(counting: Bool) -> String {
        counting
            ? "Portenv sends nothing about you or your work. You chose to be counted in usage numbers, which carry no ID."
            : "Portenv sends nothing about you or your work, and collects no usage numbers."
    }
    public static let readPrivacyPage = "Read the privacy page"
    public static let privacyURL = URL(string: "https://portenv.com/privacy")!
    public static let feedback = "Feedback"

    /// The app's own language as a Locale, so dates read as the words
    /// around them do ("12 Oct", not the Mac's "12 oct."; R-0149).
    public static func appLocale(_ preferred: [String] = Bundle.main.preferredLocalizations) -> Locale {
        Locale(identifier: preferred.first ?? "en")
    }
}

/// What the Recovery Key pane says about the recovery key (R-0137). The
/// app never keeps the key; what it has is the recovery pool (ADR 0019),
/// made from the key while first run holds it, so the pool's Keychain item
/// says whether, and when, a recovery key was made and saved on this Mac.
public enum RecoveryKeyState: Equatable, Sendable {
    case made(Date)
    case notMade
    case unreadable

    /// The pool item's creation date: nil when there's none.
    public init(poolCreated: () throws -> Date?) {
        do {
            self = try poolCreated().map(Self.made) ?? .notMade
        } catch {
            self = .unreadable
        }
    }

    /// The row's state and the line under it.
    public func row(now: Date = Date(), timeZone: TimeZone = .current, locale: Locale = SettingsText.appLocale()) -> (state: String, line: String) {
        switch self {
        case let .made(date):
            let f = DateFormatter()
            f.locale = locale
            f.timeZone = timeZone
            var cal = Calendar(identifier: .gregorian)
            cal.timeZone = timeZone
            let sameYear = cal.isDate(date, equalTo: now, toGranularity: .year)
            // Day then month, in the app's language: the board's "12 Oct".
            f.dateFormat = sameYear ? "d MMM" : "d MMM yyyy"
            // The pool is made only on Continue after "I've saved my
            // recovery key somewhere safe", so the date is when it was saved.
            return ("Saved on \(f.string(from: date))", SettingsText.recoveryLine)
        case .notMade:
            return (SettingsText.noRecoveryKey, SettingsText.noRecoveryKeyLine)
        case .unreadable:
            return (SettingsText.recoveryUnreadable, SettingsText.recoveryUnreadableLine)
        }
    }
}

extension SystemSecretStore {
    /// When the item was added to the Keychain, read from its attributes
    /// only (never its data); nil when there's none. Runs off the main
    /// thread, as every Keychain call does.
    public func createdAt(account: String) throws -> Date? {
        precondition(!Thread.isMainThread, "Keychain calls never run on the main thread")
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecMatchLimit as String: kSecMatchLimitOne,
            kSecReturnAttributes as String: true,
        ]
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        switch status {
        case errSecSuccess: return (out as? [String: Any])?[kSecAttrCreationDate as String] as? Date
        case errSecItemNotFound: return nil
        default: throw KeychainStoreError(status: status)
        }
    }
}
