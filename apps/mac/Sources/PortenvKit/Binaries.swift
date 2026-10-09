// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Where the Portenv binaries are: PORTENV_BIN_DIR, inside Portenv.app
/// (make app), or the repository's bin/ when the app runs from Xcode (make
/// build puts them there).
public enum Binaries {
    public static var directory: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_BIN_DIR"], !dir.isEmpty {
            return URL(fileURLWithPath: dir)
        }
        // Portenv.app (make app) carries its own portenv, portenvd and restic.
        let inside = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers")
        if Bundle.main.bundleURL.pathExtension == "app",
           FileManager.default.isExecutableFile(atPath: inside.appendingPathComponent("portenv").path) {
            return inside
        }
        // Run from Xcode or swift run: the repository's bin/ (make build),
        // found above the executable (apps/mac/.build/…). Not #filePath: a
        // build must never carry the path of the machine that built it.
        var dir = Bundle.main.executableURL?.deletingLastPathComponent()
        while let d = dir, d.path != "/" {
            let bin = d.appendingPathComponent("bin")
            if FileManager.default.isExecutableFile(atPath: bin.appendingPathComponent("portenv").path) {
                return bin
            }
            dir = d.deletingLastPathComponent()
        }
        return URL(fileURLWithPath: "bin")
    }
    public static var portenv: URL { directory.appendingPathComponent("portenv") }
    public static var portenvd: URL { directory.appendingPathComponent("portenvd") }

    /// The Portenv directory portenvd serves from (PORTENV_HOME, or
    /// Application Support).
    public static var home: URL {
        if let dir = ProcessInfo.processInfo.environment["PORTENV_HOME"], !dir.isEmpty {
            return URL(fileURLWithPath: dir)
        }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Portenv")
    }
    public static var socket: URL { home.appendingPathComponent("portenvd.sock") }
}
