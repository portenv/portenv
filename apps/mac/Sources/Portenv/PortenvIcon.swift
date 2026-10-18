// SPDX-License-Identifier: Apache-2.0

import AppKit

/// Portenv's own icon, loaded explicitly (R-0157): never what macOS guesses
/// for the running executable, which is a generic folder when Portenv runs
/// unbundled (swift run, the tests) and a blank app if a bundle's icon is
/// missing. Welcome, the move sheet and anything else showing "Portenv" use
/// this.
enum PortenvIcon {
    /// The asset catalogue's 512 px icon, relative to apps/mac.
    static let asset = "Resources/Assets.xcassets/AppIcon.appiconset/icon_512x512.png"

    @MainActor static var image: NSImage {
        // The app bundle's AppIcon.icns (a dev lane's carries its letter).
        if Bundle.main.bundleURL.pathExtension == "app", let icon = NSImage(named: "AppIcon") {
            return icon
        }
        // Unbundled (swift run, the tests): the asset catalogue's PNG, found
        // above the executable or a loaded test bundle (apps/mac/.build/…).
        // Not #filePath and not a SwiftPM resource (Bundle.module): both
        // would bake the build machine's paths into the app.
        if let url = unbundledAsset(), let icon = NSImage(contentsOf: url) {
            return icon
        }
        return NSApp.applicationIconImage
    }

    private final class Marker {}

    static func unbundledAsset() -> URL? {
        // The executable, and the bundle this code is in (the test bundle
        // under swift test).
        let starts = [Bundle.main.executableURL, Bundle(for: Marker.self).bundleURL].compactMap { $0?.deletingLastPathComponent() }
        for start in starts {
            var dir: URL? = start
            while let d = dir, d.path != "/" {
                for base in [d, d.appendingPathComponent("apps/mac")] {
                    let png = base.appendingPathComponent(asset)
                    if FileManager.default.fileExists(atPath: png.path) { return png }
                }
                dir = d.deletingLastPathComponent()
            }
        }
        return nil
    }
}
