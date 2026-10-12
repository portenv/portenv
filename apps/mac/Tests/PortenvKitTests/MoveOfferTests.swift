// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The launch's order with the move question (R-0121): the login item is
/// registered and the wait for portenvd starts first; the question is a
/// sheet on the window, shown alongside and never waited for.
@MainActor
struct MoveOfferTests {
    final class Log { var steps: [String] = [] }

    /// Registration, then the offer, then the wait: nothing waits on the
    /// answer (the offer only shows the sheet).
    @Test func registrationAndTheWaitDontWaitForTheAnswer() async {
        let log = Log()
        await LaunchOrder.begin(offersMove: true,
                                register: { log.steps.append("register") },
                                offer: { log.steps.append("offer") },
                                wait: { log.steps.append("wait") })
        #expect(log.steps == ["register", "offer", "wait"])
    }

    /// In Applications (or after Not Now there), no offer.
    @Test func noOfferInApplications() async {
        let log = Log()
        await LaunchOrder.begin(offersMove: false,
                                register: { log.steps.append("register") },
                                offer: { log.steps.append("offer") },
                                wait: { log.steps.append("wait") })
        #expect(log.steps == ["register", "wait"])
    }

    /// Whether to offer: outside Applications, unless Not Now was chosen
    /// for this same place.
    @Test func offeredOutsideApplicationsUnlessDeclinedHere() {
        #expect(MoveOffer.shouldOffer(placement: .elsewhere, declinedPath: nil, here: "/Users/a/Downloads/Portenv.app"))
        #expect(MoveOffer.shouldOffer(placement: .translocated, declinedPath: "/elsewhere/Portenv.app", here: "/x/Portenv.app"))
        #expect(!MoveOffer.shouldOffer(placement: .elsewhere, declinedPath: "/Users/a/Downloads/Portenv.app", here: "/Users/a/Downloads/Portenv.app"))
        #expect(!MoveOffer.shouldOffer(placement: .applications, declinedPath: nil, here: "/Applications/Portenv.app"))
    }

    /// Move: the login item registered from here goes first, so launchd
    /// never keeps running portenvd from the old place; then the copy
    /// opens (and registers from Applications).
    @Test func moveUnregistersBeforeTheCopyOpens() async {
        let log = Log()
        let error = await MoveOffer.answer(.move,
                                           unregister: { log.steps.append("unregister") },
                                           register: { log.steps.append("register") },
                                           copyAndOpen: { log.steps.append("copy") },
                                           decline: { log.steps.append("decline") })
        #expect(error == nil)
        #expect(log.steps == ["unregister", "copy"])
    }

    /// A failed copy registers the login item again here and says why.
    @Test func aFailedMoveRegistersAgain() async {
        let log = Log()
        let error = await MoveOffer.answer(.move,
                                           unregister: { log.steps.append("unregister") },
                                           register: { log.steps.append("register") },
                                           copyAndOpen: { log.steps.append("copy"); throw MoveError(MoveText.notPortenv) },
                                           decline: { log.steps.append("decline") })
        #expect(error == MoveText.notPortenv)
        #expect(log.steps == ["unregister", "copy", "register"])
    }

    /// Not Now: remembered for this place; nothing else changes.
    @Test func notNowIsRemembered() async {
        let log = Log()
        let error = await MoveOffer.answer(.notNow,
                                           unregister: { log.steps.append("unregister") },
                                           register: { log.steps.append("register") },
                                           copyAndOpen: { log.steps.append("copy") },
                                           decline: { log.steps.append("decline") })
        #expect(error == nil)
        #expect(log.steps == ["decline"])
    }

    /// The sheet's buttons (R-0124): Not Now then Move, Move the default
    /// (Return) and Not Now the cancel (Escape), so both are reachable
    /// from the keyboard and VoiceOver.
    @Test func theSheetsButtonsHaveTheirKeys() {
        #expect(MoveOffer.buttons == [
            MoveOffer.Button(title: "Not Now", answer: .notNow, key: .cancel),
            MoveOffer.Button(title: "Move to Applications", answer: .move, key: .default),
        ])
        #expect(MoveOffer.buttons.filter { $0.key == .default }.count == 1)
        #expect(MoveOffer.buttons.filter { $0.key == .cancel }.count == 1)
    }

    /// R-0112: the question's detail says workspaces.
    @Test func theDetailSaysWorkspaces() {
        #expect(MoveText.detail == "Portenv runs its background service, which keeps your workspaces saved, from the Applications folder. It can copy itself there and open again.")
    }
}
