// SPDX-License-Identifier: Apache-2.0

import Testing

@testable import PortenvKit

/// A Team sidebar row's status ("running", "stopped", "needs you") on the
/// selection (R-0411): on the active (blue) selection it's drawn in the
/// selected-content colour, since its own colour can't be read there; an
/// unselected row and the inactive (grey) selection keep the status colour.
struct TeamStatusInkTests {
    @Test func theActiveSelectionUsesTheSelectedContentColour() {
        #expect(TeamStatusInk.of(selected: true, activeSelection: true) == .selectedContent)
    }

    @Test func everyOtherRowKeepsItsStatusColour() {
        #expect(TeamStatusInk.of(selected: true, activeSelection: false) == .status)
        #expect(TeamStatusInk.of(selected: false, activeSelection: false) == .status)
        // An unselected row in a focused list is not on the selection.
        #expect(TeamStatusInk.of(selected: false, activeSelection: true) == .status)
    }
}
