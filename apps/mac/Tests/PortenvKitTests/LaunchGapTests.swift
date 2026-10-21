// SPDX-License-Identifier: Apache-2.0

import Testing

@testable import PortenvKit

/// The window is never a bare background while the launch catches up
/// (basics audit row 28, R-0386): a workspace opening before its controller
/// exists, or first run before its flow does, says what's happening.
struct LaunchGapTests {
    @Test func aWorkspaceOpeningBeforeItsControllerSaysOpening() {
        #expect(LaunchGap.line(.open("demo"), hasController: false, hasFirstRunFlow: false) == "Opening demo\u{2026}")
        #expect(LaunchGap.line(.open("demo"), hasController: true, hasFirstRunFlow: false) == nil)
    }

    @Test func firstRunBeforeItsFlowSaysStarting() {
        #expect(LaunchGap.line(.firstRun(.welcome), hasController: false, hasFirstRunFlow: false) == LaunchWait.starting)
        #expect(LaunchGap.line(.firstRun(.firstBox), hasController: false, hasFirstRunFlow: true) == nil)
    }

    @Test func otherScreensHaveNoGap() {
        #expect(LaunchGap.line(.boxList(notice: nil), hasController: false, hasFirstRunFlow: false) == nil)
        #expect(LaunchGap.line(nil, hasController: false, hasFirstRunFlow: false) == nil)
    }

    /// The box went before its open began: the list, saying so, as at launch.
    @Test func aBoxGoneBeforeItOpensGoesToTheList() {
        #expect(LaunchGap.boxGone("demo") == .boxList(notice: LaunchDecision.missing("demo")))
    }
}
