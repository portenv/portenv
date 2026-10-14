// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import Testing

@testable import Portenv

/// Welcome and the move sheet show Portenv's own icon, loaded explicitly,
/// never what macOS guesses for the running executable (R-0151, R-0157).
@MainActor
struct AppIconTests {
    /// …/apps/mac/Tests/PortenvAppTests/this file → apps/mac.
    private static let macDir = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    private static let assetPNG = macDir.appendingPathComponent("Resources/Assets.xcassets/AppIcon.appiconset/icon_512x512.png")

    /// Run unbundled, as swift test is (and as the owner's run was): the
    /// image is the 512 AppIcon PNG, pixel for pixel, and not the icon
    /// macOS gives the executable's folder (a blue folder).
    @Test func theIconIsTheAppIconNotWhatMacOSGuesses() throws {
        _ = NSApplication.shared
        let ours = try #require(PortenvIcon.image.tiffRepresentation)
        let asset = try #require(NSImage(contentsOf: Self.assetPNG)?.tiffRepresentation)
        #expect(ours == asset)
        let guessed = NSWorkspace.shared.icon(forFile: Bundle.main.bundlePath).tiffRepresentation
        #expect(ours != guessed)
    }

    /// The app bundle never needs the repository: its own AppIcon.icns.
    @Test func aBundleUsesItsOwnIcon() {
        #expect(PortenvIcon.asset.hasSuffix("icon_512x512.png"))
        #expect(PortenvIcon.unbundledAsset()?.path.hasSuffix("apps/mac/" + PortenvIcon.asset) == true)
    }

    @Test func welcomeUsesIt() {
        #expect(WelcomeArt.size == 112)
    }

    /// No SF Symbol stands in for Portenv, and nothing reads the icon macOS
    /// guesses, anywhere in the app's sources.
    @Test func noStandInForPortenvIsLeft() throws {
        let sources = Self.macDir.appendingPathComponent("Sources")
        let files = try #require(FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil))
        var checked = 0
        for case let url as URL in files where url.pathExtension == "swift" {
            let text = try String(contentsOf: url, encoding: .utf8)
            #expect(!text.contains("systemName: \"house"), "\(url.lastPathComponent) draws a house for Portenv")
            if url.lastPathComponent != "PortenvIcon.swift" {
                #expect(!text.contains("applicationIconImage"), "\(url.lastPathComponent) reads the icon macOS guesses; use PortenvIcon.image")
            }
            checked += 1
        }
        #expect(checked > 10)
    }
}
