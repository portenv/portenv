// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// ADR 0027 S5a: the hosted adapters' keys live in the Keychain, entered in
/// Settings by the owner, read by the app only at call time; never logged,
/// never shown back, never passed to portenvd, the helper or agents.
struct SmartInputKeyTests {
    /// Their own Keychain service: portenvd and the CLI read only the
    /// repository keys' service, so they can't name these.
    @Test func keysHaveTheirOwnKeychainService() throws {
        let release = try Instance(info: [:])
        #expect(release.smartInputKeyService == "dev.portenv.smart-input-keys")
        #expect(release.smartInputKeyService != release.keychainService)
        let lane = try Instance(info: ["PortenvInstance": "dev-S"])
        #expect(lane.smartInputKeyService == "dev.portenv.smart-input-keys.dev-S")
        #expect(ProviderKeyID.allCases.map(\.account) == ["anthropic", "jev"])
    }

    /// What the owner pastes is trimmed; anything that can't be a key is
    /// refused before it reaches the Keychain.
    @Test func entriesAreCleaned() {
        #expect(ProviderKeyEntry.clean("  sk-ant-abc123\n") == "sk-ant-abc123")
        #expect(ProviderKeyEntry.clean("") == nil)
        #expect(ProviderKeyEntry.clean("   ") == nil)
        #expect(ProviderKeyEntry.clean("two words") == nil)
        #expect(ProviderKeyEntry.clean("a\u{7}b") == nil)
        #expect(ProviderKeyEntry.clean(String(repeating: "k", count: 1025)) == nil)
    }

    /// A saved key is never shown back: the row says when it was saved.
    @Test func aSavedKeyIsNeverShownBack() {
        let saved = ISO8601DateFormatter().date(from: "2026-10-19T10:00:00Z")!
        let gb = Locale(identifier: "en_GB"), tz = TimeZone(identifier: "Europe/London")!
        #expect(SmartInputText.keyState(savedOn: saved, now: saved, timeZone: tz, locale: gb) == "Saved on 19 Oct")
        #expect(SmartInputText.keyState(savedOn: nil) == "Not set")
    }

    /// Only the app's smart-input files touch the key store or its
    /// service, and none of them logs. portenvd, the helper and agents
    /// (core/) never name the service.
    @Test func theKeyStaysInTheAppLayer() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let sources = root.appendingPathComponent("Sources")
        let allowed: Set = ["SmartInputKeys.swift", "AnthropicSuggester.swift", "JevClassifier.swift", "SmartInputSetup.swift",
                            "SettingsWindow.swift", "Instance.swift"]
        let fm = FileManager.default
        let files = try #require(fm.enumerator(at: sources, includingPropertiesForKeys: nil)).compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }
        #expect(files.count > 50)
        for f in files {
            let text = try String(contentsOf: f, encoding: .utf8)
            if ["ProviderKeyStore", "KeychainProviderKeys", "smartInputKeyService"].contains(where: text.contains) {
                #expect(allowed.contains(f.lastPathComponent), "\(f.lastPathComponent) reaches the provider keys")
            }
        }
        for name in ["SmartInputKeys.swift", "AnthropicSuggester.swift", "JevClassifier.swift", "SmartInputRunner.swift", "SmartInputSetup.swift"] {
            let text = try String(contentsOf: sources.appendingPathComponent("PortenvKit/\(name)"), encoding: .utf8)
            for logger in ["print(", "NSLog(", "os_log(", "Logger(", "debugPrint(", "dump("] {
                #expect(!text.contains(logger), "\(name) logs (\(logger))")
            }
        }
        let core = root.deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("core")
        let goFiles = try #require(fm.enumerator(at: core, includingPropertiesForKeys: nil)).compactMap { $0 as? URL }.filter { $0.pathExtension == "go" }
        #expect(goFiles.count > 50)
        for f in goFiles {
            #expect(!(try String(contentsOf: f, encoding: .utf8)).contains("smart-input-keys"), "\(f.lastPathComponent) names the keys' service")
        }
    }

    /// The adapters' sessions keep nothing on disk: no cache, no cookies.
    @Test func theRealSessionIsEphemeral() {
        let c = SmartInputSetup.sessionConfiguration()
        #expect(c.urlCache == nil)
        #expect(c.httpCookieStorage == nil)
        #expect(c.httpShouldSetCookies == false)
        #expect(c.requestCachePolicy == .reloadIgnoringLocalCacheData)
    }

    /// Settings decide which adapters exist: the rules always, jev and the
    /// suggester only when chosen.
    @Test func setupFollowsTheSettings() async {
        let d = UserDefaults(suiteName: "SmartInputKeyTests-\(UUID())")!
        let s = SmartInputSettings(defaults: d)
        #expect(s.classifier == .rules && s.suggester == .anthropic)
        let keys = FakeKeys()
        let net = StubNetwork(.json(200, "{}"))
        #expect(SmartInputSetup.classifier(s, keys: keys, session: net.session) == nil)
        s.classifier = .jev
        s.suggester = .none
        let again = SmartInputSettings(defaults: d)
        #expect(again.classifier == .jev && again.suggester == SuggesterChoice.none)
        #expect(SmartInputSetup.classifier(again, keys: keys, session: net.session)?.id == "jev")
        #expect(SmartInputSetup.suggester(again, keys: keys, session: net.session, commands: []) == nil)
        #expect(keys.reads == 0, "building adapters reads no key")
    }
}
