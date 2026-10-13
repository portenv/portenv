// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import Testing

@testable import Portenv

/// Welcome shows Portenv's own icon, never a stand-in symbol (R-0151).
@MainActor
struct AppIconTests {
    @Test func welcomeShowsTheApplicationIcon() {
        _ = NSApplication.shared
        let image = WelcomeArt.image
        #expect(image === NSApp.applicationIconImage)
        #expect(WelcomeArt.size == 112)
    }

    /// No SF Symbol stands in for Portenv anywhere in the app's sources.
    @Test func noStandInSymbolForPortenvIsLeft() throws {
        // …/apps/mac/Tests/PortenvAppTests/this file → apps/mac/Sources.
        let sources = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources")
        let files = try #require(FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil))
        var checked = 0
        for case let url as URL in files where url.pathExtension == "swift" {
            let text = try String(contentsOf: url, encoding: .utf8)
            #expect(!text.contains("systemName: \"house"), "\(url.lastPathComponent) draws a house for Portenv")
            checked += 1
        }
        #expect(checked > 10)
    }
}
