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

    @Test func anythingElseIsRefused() {
        for bad in ["dev", "dev-c", "dev-AB", "prod", "../x", "dev-A ", ""] {
            #expect(throws: Instance.Invalid.self) { try Instance(info: ["PortenvInstance": bad]) }
        }
    }
}
