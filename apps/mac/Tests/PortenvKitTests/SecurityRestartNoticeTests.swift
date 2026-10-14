// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
import Testing

@testable import PortenvKit

/// portenvd restarted a workspace left running with mounts it shouldn't
/// have (R-0201). The window says so once, in plain words, and not again.
@MainActor
struct SecurityRestartNoticeTests {
    @Test func theWindowSaysSoOnceAndNotAgain() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli, keychain: BlockingKeychain(blocks: false))
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-14T10:00:00Z","restarted_for_security_update":true}"#
        await c.open()
        await c.refresh()
        #expect(c.notice == BoxController.securityRestartNotice)
        #expect(BoxController.securityRestartNotice == "Portenv restarted this workspace to apply a security update. Your files and saves are as you left them; programs that were running were stopped.")
        c.dismissNotice()
        #expect(c.notice == nil)
        // The state still says it for this open: the window doesn't again.
        await c.refresh()
        await c.refresh()
        #expect(c.notice == nil, "told once, not again")
    }

    @Test func anOrdinaryOpenSaysNothing() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli, keychain: BlockingKeychain(blocks: false))
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-14T10:00:00Z"}"#
        await c.open()
        await c.refresh()
        #expect(c.notice == nil)
    }

    @Test func theStateCarriesIt() {
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_SAVED","restarted_for_security_update":true}"#)?.restartedForSecurityUpdate == true)
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_SAVED"}"#)?.restartedForSecurityUpdate == false)
        var r = Portenv_Daemon_V1_GetBoxStateResponse()
        r.state = .saved
        r.restartedForSecurityUpdate = true
        #expect(BoxState(r)?.restartedForSecurityUpdate == true)
    }
}
