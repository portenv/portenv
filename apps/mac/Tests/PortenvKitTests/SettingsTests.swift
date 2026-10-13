// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Portenv › Settings… against the settings board (R-0137).
struct SettingsTests {
    let tz = TimeZone(identifier: "Europe/Paris")!
    let gb = Locale(identifier: "en_GB")
    let saved = ISO8601DateFormatter().date(from: "2026-10-12T19:00:00Z")!

    @Test func fivePanesInTheBoardsOrder() {
        #expect(SettingsPane.allCases.map(\.title) == ["General", "Servers", "Agents", "Recovery Key", "Updates"])
    }

    /// The pool's Keychain item says whether a recovery key was made and
    /// saved here (ADR 0019); its date is what the row says.
    @Test func theRecoveryKeyRowFollowsThePool() {
        let made = RecoveryKeyState { saved }
        #expect(made == .made(saved))
        #expect(made.row(now: saved.addingTimeInterval(86400), timeZone: tz, locale: gb).state == "Saved on 12 Oct")
        #expect(made.row(now: saved.addingTimeInterval(400 * 86400), timeZone: tz, locale: gb).state == "Saved on 12 Oct 2026")
        #expect(made.row(timeZone: tz, locale: gb).line == SettingsText.recoveryLine)
        #expect(RecoveryKeyState { nil } == .notMade)
        #expect(RecoveryKeyState { nil }.row().state == SettingsText.noRecoveryKey)
    }

    /// A Keychain error never reads as "no key": it says it couldn't check.
    @Test func aKeychainErrorSaysItCouldntCheck() {
        let state = RecoveryKeyState { throw KeychainStoreError(status: -25308) }
        #expect(state == .unreadable)
        #expect(state.row().state == SettingsText.recoveryUnreadable)
    }

    /// On unless turned off, under Sparkle's own key so it holds once
    /// Sparkle reads it.
    @Test func automaticChecksAreOnUntilTurnedOff() throws {
        let defaults = try #require(UserDefaults(suiteName: "SettingsTests-\(UUID().uuidString)"))
        #expect(UpdateSettings.automaticChecksKey == "SUEnableAutomaticChecks")
        #expect(UpdateSettings.automaticChecks(defaults))
        UpdateSettings.setAutomaticChecks(false, defaults)
        #expect(!UpdateSettings.automaticChecks(defaults))
        #expect(defaults.object(forKey: "SUEnableAutomaticChecks") as? Bool == false)
    }

    @Test func theVersionRow() {
        let facts = Feedback.Facts(version: "0.2.0", build: "112", commit: nil, macOS: "26.0", chip: "Apple M4")
        #expect(SettingsText.version(facts) == "Version 0.2.0 (112)")
    }

    /// The SSH door ships off and marked PREVIEW (the owner, R-0102); the
    /// copy says so, with typographic apostrophes.
    @Test func theSSHDoorSaysItsAPreviewAndOff() {
        #expect(SettingsText.preview == "PREVIEW")
        #expect(SettingsText.allowAgentsLine.hasPrefix("Off."))
        #expect(!SettingsText.previewNote.contains("'"))
    }
}
