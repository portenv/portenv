// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

/// SecKeychainSetUserInteractionAllowed(false) turns Keychain prompts off
/// for the whole process. The app is where prompts must work (the person
/// approves reads there), so no Swift code in the repository may call it.
/// Only the Go daemon side does (core/keys). `make app` also checks the
/// built executable doesn't import it.
struct NoPromptSwitchTests {
    @Test func noSwiftCodeCallsTheSwitch() throws {
        // …/apps/mac/Tests/PortenvKitTests/this file → the repository root.
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let thisFile = URL(fileURLWithPath: #filePath).standardizedFileURL.path
        var scanned = 0
        for dir in ["apps", "shims"] {
            let base = root.appendingPathComponent(dir)
            guard let walk = FileManager.default.enumerator(at: base, includingPropertiesForKeys: nil) else { continue }
            for case let url as URL in walk {
                if url.lastPathComponent == ".build" { walk.skipDescendants(); continue }
                guard ["swift", "m", "h", "c"].contains(url.pathExtension),
                      url.standardizedFileURL.path != thisFile else { continue }
                scanned += 1
                let text = try String(contentsOf: url, encoding: .utf8)
                #expect(!text.contains("SecKeychainSetUserInteractionAllowed"),
                        "\(url.path) turns Keychain prompts off process-wide; the app must be able to prompt")
            }
        }
        #expect(scanned > 10, "found the app's sources")
    }
}
