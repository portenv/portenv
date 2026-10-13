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

    /// What Portenv sends follows the real counting setting (R-0149):
    /// nothing about the person or their work, and usage numbers only if
    /// they chose to be counted.
    @Test func whatPortenvSendsFollowsTheCountingSetting() {
        let off = SettingsText.whatPortenvSendsLine(counting: false)
        let on = SettingsText.whatPortenvSendsLine(counting: true)
        #expect(off == "Portenv sends nothing about you or your work, and collects no usage numbers.")
        #expect(on.hasPrefix("Portenv sends nothing about you or your work."))
        #expect(on.contains("usage numbers") && !on.contains("no usage numbers"))
        #expect(!off.contains("update check") && !on.contains("update check"))
    }

    /// Counting is on only in a build with counting, and only if chosen;
    /// this build has none, so a stored choice alone never turns it on.
    @Test func countingIsOffWithoutABuildThatCounts() throws {
        let defaults = try #require(UserDefaults(suiteName: "SettingsTests-\(UUID().uuidString)"))
        defaults.set(true, forKey: UsageCounting.choiceKey)
        #expect(UsageCounting.isOn(defaults) == UsageCounting.shown)
        defaults.set(false, forKey: UsageCounting.choiceKey)
        #expect(!UsageCounting.isOn(defaults))
    }

    /// The date is in the app's language, whatever the Mac's is: "12 Oct",
    /// not fr_FR's "12 oct." (R-0149).
    @Test func theDateFollowsTheAppsLanguageNotTheMacs() {
        let made = RecoveryKeyState.made(saved)
        let later = saved.addingTimeInterval(86400)
        #expect(SettingsText.appLocale(["en"]).identifier == "en")
        #expect(made.row(now: later, timeZone: tz, locale: SettingsText.appLocale(["en"])).state == "Saved on 12 Oct")
        // The default, too: the app's language (this test bundle is in
        // English), not Locale.current, whatever the Mac is set to.
        #expect(made.row(now: later, timeZone: tz).state == "Saved on 12 Oct")
        // What a French Mac's locale would have given.
        #expect(made.row(now: later, timeZone: tz, locale: Locale(identifier: "fr_FR")).state != "Saved on 12 Oct")
    }

    @Test func theVersionRow() {
        let facts = Feedback.Facts(version: "0.2.0", build: "112", commit: nil, macOS: "26.0", chip: "Apple M4")
        #expect(SettingsText.version(facts) == "Version 0.2.0 (112)")
    }

    /// Until 2.4 and AddRecoveryKey, rows that would offer what isn't
    /// built aren't there at all (R-0140, R-0149).
    @Test func nothingUnbuiltIsOffered() {
        #expect(SettingsText.downloadForNow.contains("download"))
        #expect(!SettingsText.downloadForNow.contains("Up to date"))
    }
}
