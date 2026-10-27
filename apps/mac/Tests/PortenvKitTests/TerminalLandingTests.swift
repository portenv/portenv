// SPDX-License-Identifier: Apache-2.0

import Testing

@testable import PortenvKit

/// Where a tab's terminal lands when the owner switches to it (the owner,
/// 2026-10-22; GUIDELINES §4.1): at the live prompt, unless they scrolled up
/// in that tab themselves and nothing new has arrived there since.
struct TerminalLandingTests {
    @Test func aSwitchLandsAtThePromptUnlessTheOwnersPlaceStillHolds() {
        // Not scrolled up: the prompt.
        #expect(TerminalLanding.on(switched: true, scrolledUp: false, newOutputSince: false) == .bottom)
        #expect(TerminalLanding.on(switched: true, scrolledUp: false, newOutputSince: true) == .bottom)
        // Scrolled up, and something new arrived since: the prompt.
        #expect(TerminalLanding.on(switched: true, scrolledUp: true, newOutputSince: true) == .bottom)
        // Scrolled up, nothing new: their place.
        #expect(TerminalLanding.on(switched: true, scrolledUp: true, newOutputSince: false) == .keep)
    }

    /// No switch, no move: output while the owner reads back never pulls
    /// the view down.
    @Test func withoutASwitchNothingMoves() {
        for up in [false, true] {
            for new in [false, true] {
                #expect(TerminalLanding.on(switched: false, scrolledUp: up, newOutputSince: new) == .keep)
            }
        }
    }
}
