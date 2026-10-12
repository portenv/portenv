// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Move to Applications during one launch (R-0125, R-0130): no workspace
/// opens while the question shows, so Move never stops portenvd under an
/// open workspace; nothing registers the old place again during a move;
/// the question comes once per launch.
@MainActor
struct MoveGateTests {
    final class Log { var steps: [String] = [] }

    /// A workspace that would open while the question shows waits for the
    /// answer; Not Now opens it then.
    @Test func noWorkspaceOpensWhileTheQuestionShows() async {
        let gate = MoveGate()
        #expect(gate.offer(if: true))
        #expect(gate.open("acme-api") == nil) // held
        let log = Log()
        let result = await gate.answer(.notNow, workspaceOpen: false,
                                       stopWait: { log.steps.append("stop") },
                                       unregister: { log.steps.append("unregister") },
                                       register: { log.steps.append("register") },
                                       copyAndOpen: { log.steps.append("copy") },
                                       decline: { log.steps.append("decline") })
        #expect(result == MoveGate.Result(error: nil, open: "acme-api"))
        #expect(log.steps == ["decline"])
        #expect(!gate.offering)
        #expect(gate.open("site") == "site") // answered: opens at once
    }

    /// No question, no hold.
    @Test func withoutTheQuestionWorkspacesOpenAtOnce() {
        let gate = MoveGate()
        #expect(!gate.offer(if: false))
        #expect(gate.open("acme-api") == "acme-api")
    }

    /// Move stops the launch wait, then unregisters, then copies; while it
    /// moves, registering is off, so nothing registers the old place again.
    @Test func moveStopsTheWaitAndRegistrationFirst() async {
        let gate = MoveGate()
        _ = gate.offer(if: true)
        _ = gate.open("acme-api")
        let log = Log()
        let result = await gate.answer(.move, workspaceOpen: false,
                                       stopWait: { log.steps.append("stop") },
                                       unregister: {
                                           #expect(!gate.registrationAllowed)
                                           log.steps.append("unregister")
                                       },
                                       register: { log.steps.append("register") },
                                       copyAndOpen: {
                                           #expect(!gate.registrationAllowed)
                                           log.steps.append("copy")
                                       },
                                       decline: { log.steps.append("decline") })
        #expect(log.steps == ["stop", "unregister", "copy"])
        #expect(result == MoveGate.Result(error: nil, open: nil)) // the copy opens it
        #expect(!gate.registrationAllowed) // still moving: the app is quitting
    }

    /// A failed copy turns registering back on before registering here
    /// again, says why, and opens what was held.
    @Test func aFailedMoveRegistersAgainAndOpensTheHeldWorkspace() async {
        let gate = MoveGate()
        _ = gate.offer(if: true)
        _ = gate.open("acme-api")
        let log = Log()
        let result = await gate.answer(.move, workspaceOpen: false,
                                       stopWait: { log.steps.append("stop") },
                                       unregister: { log.steps.append("unregister") },
                                       register: {
                                           #expect(gate.registrationAllowed)
                                           log.steps.append("register")
                                       },
                                       copyAndOpen: { log.steps.append("copy"); throw MoveError(MoveText.notPortenv) },
                                       decline: { log.steps.append("decline") })
        #expect(log.steps == ["stop", "unregister", "copy", "register"])
        #expect(result == MoveGate.Result(error: MoveText.notPortenv, open: "acme-api"))
        #expect(gate.registrationAllowed)
    }

    /// Belt and braces: with a workspace open anyway, Move does nothing to
    /// the login item and says why; the offer comes back next launch.
    @Test func moveNeverRunsWithAWorkspaceOpen() async {
        let gate = MoveGate()
        _ = gate.offer(if: true)
        let log = Log()
        let result = await gate.answer(.move, workspaceOpen: true,
                                       stopWait: { log.steps.append("stop") },
                                       unregister: { log.steps.append("unregister") },
                                       register: { log.steps.append("register") },
                                       copyAndOpen: { log.steps.append("copy") },
                                       decline: { log.steps.append("decline") })
        #expect(log.steps.isEmpty)
        #expect(result.error == MoveText.workspaceOpen)
        #expect(gate.registrationAllowed)
    }

    /// Once per launch: Try Again's second start doesn't ask again.
    @Test func offeredOncePerLaunch() async {
        let gate = MoveGate()
        #expect(gate.offer(if: true))
        _ = await gate.answer(.notNow, workspaceOpen: false, stopWait: {}, unregister: {}, register: {},
                              copyAndOpen: {}, decline: {})
        #expect(!gate.offer(if: true))
        #expect(!gate.offering)
    }

    /// The words (R-0112: workspaces).
    @Test func theWords() {
        #expect(MoveText.workspaceOpen == "Portenv can\u{2019}t move while a workspace is open. It will offer again the next time it opens.")
    }
}
