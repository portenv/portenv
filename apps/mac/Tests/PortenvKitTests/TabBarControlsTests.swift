// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The tab bar's `+` and split buttons (GUIDELINES.md §4.1, §6.2): in an
/// agent's view the tab strip has no `+` button: agents open their own
/// tabs (owner, 2026-10-21). The split buttons are You's alone, in the tab
/// bar, and Split Right and Split Down are off in an agent's view: agents
/// split their own panes (owner, 2026-10-21, R-0410).
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

    @Test func theSplitButtonsAreOnlyYours() {
        #expect(TabBarControls.showsSplitButtons(for: .you))
        #expect(TabBarControls.showsSplitButtons(for: .agent("builder")) == false)
        #expect(TabBarControls.showsSplitButtons(for: .allTabs) == false)
        #expect(TabBarControls.showsSplitButtons(for: .activity) == false)
        #expect(TabBarControls.showsSplitButtons(for: .decision("first-start")) == false)
    }

    @Test func theSplitMenuItemsAreOffOnlyInAnAgentsView() {
        #expect(TabBarControls.allowsSplit(for: .agent("builder")) == false)
        #expect(TabBarControls.allowsSplit(for: .you))
        #expect(TabBarControls.allowsSplit(for: .allTabs))
        #expect(TabBarControls.allowsSplit(for: .activity))
        #expect(TabBarControls.allowsSplit(for: .decision("first-start")))
    }

    @Test func theSplitButtonsFollowTheSidebarsSelection() async {
        let c = TabsController(box: "acme-api", daemon: FakeDaemon())
        #expect(c.showsSplitButtons == false) // All tabs
        await c.view(.you)?.value
        #expect(c.showsSplitButtons)
        await c.view(.agent("builder"))?.value
        #expect(c.showsSplitButtons == false)
    }
}
