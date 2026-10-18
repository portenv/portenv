// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
import Testing

@testable import PortenvKit

/// A tab's command line, for the Move confirmation (2.3), can hold a
/// secret (mysql -p…). It's shown on this Mac only, as one line, and never
/// printed, logged, sent in feedback or kept (R-0140).
@MainActor
struct TabCommandTests {
    private let secret = TabInfo(id: "@0", name: "db", active: true, program: "mysql",
                                 command: "mysql -u root -pSECRET", runningSince: Date(timeIntervalSince1970: 1_791_900_000))

    /// Printing a tab, in a log line, an assertion message or a crash
    /// breadcrumb, never carries its command.
    @Test func aTabNeverPrintsItsCommand() {
        for text in [String(describing: secret), String(reflecting: secret), "\(secret)", "\([secret])", "\(Mirror(reflecting: secret).children.map { "\($0.value)" })"] {
            #expect(!text.contains("SECRET"), "\(text)")
        }
        #expect(secret.command == "mysql -u root -pSECRET") // the sheet reads it directly
    }

    /// Following the tabs keeps nothing on disk: no default holds the
    /// command; and Send Feedback's message is only the app's facts.
    @Test func nothingKeepsOrSendsIt() async throws {
        let daemon = FakeDaemon()
        daemon.setTabs([secret])
        let tabs = TabsController(box: "acme-api", daemon: daemon, retry: .milliseconds(10))
        await tabs.load()
        #expect(tabs.tabs.first?.command == "mysql -u root -pSECRET")
        let defaults = "\(UserDefaults.standard.dictionaryRepresentation())"
        #expect(!defaults.contains("SECRET"))
        let facts = Feedback.Facts(version: "0.1.0", build: "70", commit: nil, macOS: "26.0", chip: "Apple M4")
        #expect(!Feedback.body(facts).contains("SECRET"))
        #expect(!Feedback.mailto(facts).absoluteString.contains("SECRET"))
    }

    /// The client reads the command and its start from the wire.
    @Test func theClientReadsThem() {
        var t = Portenv_Types_V1_Tab()
        t.id = "@1"
        t.program = "mysql"
        t.command = "mysql -pSECRET"
        t.runningSince = .init(date: Date(timeIntervalSince1970: 100))
        let info = TabInfo(t)
        #expect(info.command == "mysql -pSECRET")
        #expect(info.runningSince == Date(timeIntervalSince1970: 100))
    }
}
