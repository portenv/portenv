// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Help › Send Feedback… (R-0088): the message carries the app's version,
/// the macOS version and the chip, and nothing else.
struct FeedbackTests {
    private let sample = Feedback.Facts(version: "0.1.0", build: "42", commit: "abc1234",
                                        macOS: "26.0.1", chip: "Apple M2")

    @Test func bodyIsTwoEmptyLinesThenOnlyTheFacts() {
        #expect(Feedback.body(sample) == "\n\n--\nPortenv 0.1.0 (build 42)\nCommit abc1234\nmacOS 26.0.1\nApple M2\n")
    }

    /// Dev builds have no PortenvCommit key: no commit line at all.
    @Test func commitLineOnlyWhenTheBuildHasOne() {
        var facts = sample
        facts.commit = nil
        #expect(Feedback.body(facts) == "\n\n--\nPortenv 0.1.0 (build 42)\nmacOS 26.0.1\nApple M2\n")
        let dev = Feedback.Facts(info: ["CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "1"],
                                 macOS: "26.0", chip: "Apple M2")
        #expect(dev.commit == nil)
        #expect(!Feedback.body(dev).contains("Commit"))
        let release = Feedback.Facts(info: ["CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "1",
                                            Feedback.commitKey: "abc1234"],
                                     macOS: "26.0", chip: "Apple M2")
        #expect(Feedback.body(release).contains("\nCommit abc1234\n"))
    }

    /// Facts holds exactly the allowed fields, and building it from an
    /// Info.plist reads only the version, build and commit keys: a home
    /// path, user or host name in the dictionary can't reach the message.
    @Test func onlyTheAllowedFieldsCanReachTheMessage() {
        let fields = Mirror(reflecting: sample).children.compactMap(\.label)
        #expect(fields == ["version", "build", "commit", "macOS", "chip"])

        let info: [String: Any] = [
            "CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "42",
            "NSHomeDirectory": "/Users/someone", "USER": "someone", "HostName": "someones-mac",
            "PORTENV_BOX": "secret-box", "IOPlatformSerialNumber": "C02XYZ",
        ]
        let facts = Feedback.Facts(info: info, macOS: "26.0.1", chip: "Apple M2")
        let body = Feedback.body(facts)
        for leak in ["/Users", "someone", "secret-box", "C02XYZ"] {
            #expect(!body.contains(leak), "\(leak) reached the message")
        }
        #expect(body == "\n\n--\nPortenv 0.1.0 (build 42)\nmacOS 26.0.1\nApple M2\n")
    }

    /// Subject and body come back exactly through URLComponents, even
    /// with "+", "&", "=", "?", "#", newlines and non-ASCII; "+" is
    /// encoded, since some mail apps read a bare "+" as a space.
    @Test func mailtoRoundTripsSubjectAndBody() throws {
        let tricky = Feedback.Facts(version: "1.0+beta&x=y?z#w", build: "4 2", commit: "é😀",
                                    macOS: "26.0\nline", chip: "Apple M2 % done")
        let url = Feedback.mailto(tricky)
        #expect(url.scheme == "mailto")
        let parts = try #require(URLComponents(url: url, resolvingAgainstBaseURL: false))
        #expect(parts.path == Feedback.address)
        let items = try #require(parts.queryItems)
        #expect(items.map(\.name) == ["subject", "body"])
        #expect(items[0].value == Feedback.subject)
        #expect(items[1].value == Feedback.body(tricky))

        let query = try #require(url.absoluteString.split(separator: "?", maxSplits: 1).last)
        #expect(!query.contains("+"))
        #expect(query.split(separator: "&").count == 2)
        #expect(query.contains("%2B") && query.contains("%26") && query.contains("%3D") && query.contains("%0A"))
    }

    @Test func addressLivesInOnePlace() {
        #expect(Feedback.mailto(sample).absoluteString.hasPrefix("mailto:\(Feedback.address)?"))
        #expect(Feedback.noMailAppDetail.contains(Feedback.address))
    }

    @Test func macOSVersionIsShort() {
        #expect(Feedback.macOS(OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 1)) == "26.0.1")
        #expect(Feedback.macOS(OperatingSystemVersion(majorVersion: 26, minorVersion: 1, patchVersion: 0)) == "26.1")
    }

    /// This Mac's chip, like "Apple M2".
    @Test func chipIsReadFromThisMac() throws {
        let chip = try #require(Feedback.chip())
        #expect(!chip.isEmpty && !chip.contains("\0"))
    }
}
