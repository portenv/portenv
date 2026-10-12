// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
import Testing

/// Help › Acknowledgements… reads Contents/Resources/ThirdPartyNotices.txt
/// (R-0096), which gmake app writes.
struct AcknowledgementsTests {
    /// A minimal app bundle in a temporary folder, with or without notices.
    private func bundle(notices: String?) throws -> Bundle {
        let app = FileManager.default.temporaryDirectory
            .appendingPathComponent("ack-\(UUID().uuidString).app")
        let resources = app.appendingPathComponent("Contents/Resources")
        try FileManager.default.createDirectory(at: resources, withIntermediateDirectories: true)
        let plist: [String: Any] = ["CFBundleIdentifier": "com.portenv.test.ack-\(UUID().uuidString)",
                                    "CFBundlePackageType": "APPL"]
        try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
            .write(to: app.appendingPathComponent("Contents/Info.plist"))
        if let notices {
            try Data(notices.utf8).write(to: resources.appendingPathComponent("ThirdPartyNotices.txt"))
        }
        return try #require(Bundle(url: app))
    }

    @Test func showsTheNoticesFromTheApp() throws {
        let notices = "Portenv includes the following third-party software.\n\n== github.com/restic/restic v0.19.1 (BSD-2-Clause)\n"
        #expect(Acknowledgements.text(in: try bundle(notices: notices)) == notices)
    }

    @Test func saysSoWhenTheBuildHasNone() throws {
        #expect(Acknowledgements.text(in: try bundle(notices: nil)) == Acknowledgements.missing)
        #expect(Acknowledgements.text(in: try bundle(notices: "")) == Acknowledgements.missing)
    }

    @Test func menuItemOpensAWindow() {
        // An ellipsis: the item opens a window (GUIDELINES §10).
        #expect(Acknowledgements.menuItem == "Acknowledgements…")
        #expect(Acknowledgements.windowTitle == "Acknowledgements")
    }
}
