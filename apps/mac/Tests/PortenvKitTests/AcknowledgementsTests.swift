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

    /// The list of components, one per line, is what gets the hanging
    /// indent; the intro and the sections don't (R-0119).
    @Test func listRangeIsTheComponentLines() throws {
        let notices = "Portenv includes the following third-party software. Each component's\nlicence follows below.\n\n"
            + "cel.dev/expr v0.25.1 · Apache-2.0\ngithub.com/Azure/azure-sdk-for-go/sdk/azidentity v1.13.1 · MIT\n"
            + "\n\n== cel.dev/expr v0.25.1 (Apache-2.0)\nSource: https://proxy.golang.org/cel.dev/expr/@v/v0.25.1.zip\n"
        let range = try #require(Acknowledgements.listRange(in: notices))
        #expect((notices as NSString).substring(with: range)
            == "cel.dev/expr v0.25.1 · Apache-2.0\ngithub.com/Azure/azure-sdk-for-go/sdk/azidentity v1.13.1 · MIT\n")
        #expect(Acknowledgements.listRange(in: Acknowledgements.missing) == nil)
    }

    /// Edit › Find uses the standard names (R-0119).
    @Test func findMenuUsesTheStandardNames() {
        #expect([FindMenu.menu, FindMenu.find, FindMenu.findNext, FindMenu.findPrevious, FindMenu.useSelection]
            == ["Find", "Find…", "Find Next", "Find Previous", "Use Selection for Find"])
    }

    @Test func menuItemOpensAWindow() {
        // An ellipsis: the item opens a window (GUIDELINES §10).
        #expect(Acknowledgements.menuItem == "Acknowledgements…")
        #expect(Acknowledgements.windowTitle == "Acknowledgements")
    }
}
