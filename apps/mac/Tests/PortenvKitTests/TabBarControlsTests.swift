// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The tab bar's `+` (GUIDELINES.md §4.1, §6.2): in an agent's view the
/// tab strip has no `+` button: agents open their own tabs (owner,
/// 2026-10-21). You, All tabs, Team activity and a decision keep it.
@MainActor
struct TabBarControlsTests {
    @Test func anAgentsViewHasNoNewTabButton() {
        #expect(TabBarControls.showsNewTabButton(for: .agent("builder")) == false)
        #expect(TabBarControls.showsNewTabButton(for: .agent("lead")) == false)
    }

    @Test func youAndEveryOtherViewKeepTheNewTabButton() {
        #expect(TabBarControls.showsNewTabButton(for: .you))
        #expect(TabBarControls.showsNewTabButton(for: .allTabs))
        #expect(TabBarControls.showsNewTabButton(for: .activity))
        #expect(TabBarControls.showsNewTabButton(for: .decision("first-start")))
    }

    @Test func theTabBarFollowsTheSidebarsSelection() async {
        let c = TabsController(box: "acme-api", daemon: FakeDaemon())
        #expect(c.showsNewTabButton)
        await c.view(.agent("builder"))?.value
        #expect(c.showsNewTabButton == false)
        await c.view(.you)?.value
        #expect(c.showsNewTabButton)
        await c.view(.agent("builder"))?.value
        await c.view(.allTabs)?.value
        #expect(c.showsNewTabButton)
    }
}
