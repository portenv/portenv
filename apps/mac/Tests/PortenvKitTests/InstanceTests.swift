// SPDX-License-Identifier: Apache-2.0

@testable import PortenvKit
import Testing

/// A release build keeps today's names; a dev lane's build (gmake app
/// LANE=X stamps PortenvInstance=dev-X) gets its own launch agent, Keychain
/// service and support folder, the same names core/internal/instance uses.
struct InstanceTests {
    @Test func releaseKeepsTodaysNames() throws {
        let i = try Instance(info: ["CFBundleIdentifier": "com.portenv.Portenv"])
        #expect(i.name == nil)
        #expect(i.daemonPlistName == "com.portenv.portenvd.plist")
        #expect(i.daemonLabel == "com.portenv.portenvd")
        #expect(i.keychainService == "dev.portenv.repository")
        #expect(i.folder == "Portenv")
        #expect(i.environment == [:])
    }

    @Test func aDevLaneGetsItsOwnNames() throws {
        let i = try Instance(info: ["PortenvInstance": "dev-C"])
        #expect(i.name == "dev-C")
        #expect(i.daemonPlistName == "com.portenv.portenvd.dev-C.plist")
        #expect(i.daemonLabel == "com.portenv.portenvd.dev-C")
        #expect(i.keychainService == "dev.portenv.repository.dev-C")
        #expect(i.folder == "Portenv-dev-C")
        #expect(i.environment == ["PORTENV_INSTANCE": "dev-C"])
    }

    /// A release build never reads PORTENV_INSTANCE, so setting it can't
    /// move the app onto a dev Keychain service or folder.
    @Test func releaseIgnoresTheEnvironment() throws {
        let i = try Instance(info: [:])
        for v in ["dev-B", "nonsense", ""] {
            #expect(i.mismatch(environment: ["PORTENV_INSTANCE": v]) == nil)
        }
    }

    /// A dev build carries its lane in Info.plist; the environment may only
    /// agree with it.
    @Test func aDevBuildRefusesAMismatch() throws {
        let i = try Instance(info: ["PortenvInstance": "dev-C"])
        #expect(i.mismatch(environment: [:]) == nil)
        #expect(i.mismatch(environment: ["PORTENV_INSTANCE": "dev-C"]) == nil)
        #expect(i.mismatch(environment: ["PORTENV_INSTANCE": "dev-B"])
            == "This is the dev-C build of Portenv, but PORTENV_INSTANCE is dev-B. Unset it, or open the dev-B build.")
    }

    @Test func anythingElseIsRefused() {
        for bad in ["dev", "dev-c", "dev-AB", "prod", "../x", "dev-A ", ""] {
            #expect(throws: Instance.Invalid.self) { try Instance(info: ["PortenvInstance": bad]) }
        }
    }
}
