// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Testing

@testable import PortenvKit

/// The terminal always shows the tab the tab bar marks current, whatever
/// the Team sidebar selects (R-0314): with Code Writer selected, the owner's
/// typing can never reach Lead's pane. Each step does what the window does
/// on a change (MainWindow.choose: the selection, then the panes), and then
/// checks the tab MainTabTerminal is given (the focused pane's) against the
/// bar's current tab and the selection.
@MainActor
@Suite(.serialized)
struct TerminalFollowsSelectionTests {
    let team = [TeamAgent(id: "lead", name: "Lead", status: .running),
                TeamAgent(id: "writer", name: "Code Writer", status: .running),
                TeamAgent(id: "builder", name: "Builder", status: .stopped)]

    /// The owner's two tabs, then Lead's and Code Writer's, as portenvd
    /// lists them; `active` is the one portenvd shows this app.
    func list(active: String, without: Set<String> = [], adding extra: [TabInfo] = []) -> [TabInfo] {
        let all = [TabInfo(id: "@1", name: "shell", active: false),
                   TabInfo(id: "@2", name: "server", active: false),
                   TabInfo(id: "lead:@1", name: "lead", active: false, agentID: "lead"),
                   TabInfo(id: "writer:@1", name: "writer", active: false, agentID: "writer"),
                   TabInfo(id: "writer:@2", name: "notes", active: false, agentID: "writer")] + extra
        return all.filter { !without.contains($0.id) }.map { t in
            var t = t
            t.active = t.id == active
            return t
        }
    }

    func make(active: String = "@1") -> (TabsController, SplitController, FakeDaemon) {
        let daemon = FakeDaemon()
        daemon.setTabs(list(active: active))
        let tabs = TabsController(box: "acme-api", daemon: daemon)
        tabs.receive(list(active: active))
        let split = SplitController(tabs: tabs)
        split.size = CGSize(width: 1400, height: 900)
        split.tabsChanged()
        return (tabs, split, daemon)
    }

    /// What the window does when the owner clicks in the sidebar.
    func choose(_ s: TeamSelection, _ tabs: TabsController, _ split: SplitController) async {
        await tabs.view(s)?.value
        split.tabsChanged()
    }

    /// What the window does when the tabs change underneath.
    func receive(_ ts: [TabInfo], _ tabs: TabsController, _ split: SplitController) async {
        tabs.receive(ts)
        split.tabsChanged()
        await split.settled()
        await Task.yield()
        split.tabsChanged()
    }

    func selects(_ d: FakeDaemon) -> [String] { d.calls.filter { $0.count > 3 && $0[1] == "select-tab" }.map { $0[3] } }

    /// The terminal (the focused pane) shows `id`, the bar marks `id` and
    /// only `id`, and the tab is among those the bar shows.
    func expectShows(_ id: String, _ tabs: TabsController, _ split: SplitController,
                     sourceLocation: SourceLocation = #_sourceLocation) {
        #expect(split.activeTab == id, "the tab MainTabTerminal is given", sourceLocation: sourceLocation)
        #expect(tabs.terminalTab?.id == id, "the tab the bar marks current", sourceLocation: sourceLocation)
        #expect(split.agreesWithBar, sourceLocation: sourceLocation)
        #expect(tabs.shownTabs.contains { $0.id == id }, "in the bar", sourceLocation: sourceLocation)
        #expect(tabs.shownTabs.filter { $0.id == tabs.terminalTab?.id }.count == 1, sourceLocation: sourceLocation)
    }

    /// The owner's report (R-0314): Lead's tab was current and Code Writer
    /// selected; the terminal must switch to Code Writer's tab, not stay on
    /// Lead's.
    @Test func selectingAnAgentShowsItsTabNotTheLastOne() async {
        let (tabs, split, d) = make(active: "lead:@1")
        expectShows("lead:@1", tabs, split)
        await choose(.agent("writer"), tabs, split)
        expectShows("writer:@1", tabs, split)
        #expect(tabs.shownTabs.map(\.id) == ["writer:@1", "writer:@2"])
        #expect(selects(d) == ["writer:@1"], "portenvd shows it too, so typing goes there")
        #expect(d.calls.contains { $0.count > 3 && $0[1] == "select-tab" && $0[3].hasPrefix("lead") } == false)
    }

    /// CC-0491: selecting an agent from the owner's own tab doesn't leave
    /// the owner's tab in the terminal while the bar shows only the agent's.
    @Test func selectingAnAgentFromYourOwnTab() async {
        let (tabs, split, _) = make(active: "@2")
        await choose(.agent("lead"), tabs, split)
        expectShows("lead:@1", tabs, split)
    }

    @Test func everyTransitionKeepsTheTerminalOnTheSelection() async {
        let (tabs, split, _) = make(active: "@2")
        // An agent, then another.
        await choose(.agent("writer"), tabs, split)
        expectShows("writer:@1", tabs, split)
        await tabs.select("writer:@2")
        split.tabsChanged()
        expectShows("writer:@2", tabs, split)
        await choose(.agent("lead"), tabs, split)
        expectShows("lead:@1", tabs, split)
        // Back to Code Writer: the tab it last showed.
        await choose(.agent("writer"), tabs, split)
        expectShows("writer:@2", tabs, split)
        // All tabs: the owner's tab, the one last shown.
        await choose(.allTabs, tabs, split)
        expectShows("@2", tabs, split)
        // You, from an agent.
        await choose(.agent("lead"), tabs, split)
        await choose(.you, tabs, split)
        expectShows("@2", tabs, split)
        #expect(tabs.shownTabs.map(\.id) == ["@1", "@2"])
    }

    /// The tab on show closes (the agent closed it; portenvd goes back to
    /// the owner's tab): the terminal moves to the agent's other tab, never
    /// to the owner's or another agent's.
    @Test func anAgentsTabClosing() async {
        let (tabs, split, d) = make(active: "@1")
        await choose(.agent("writer"), tabs, split)
        await tabs.select("writer:@2")
        split.tabsChanged()
        expectShows("writer:@2", tabs, split)
        let after = list(active: "@1", without: ["writer:@2"])
        d.setTabs(after)
        await receive(after, tabs, split)
        expectShows("writer:@1", tabs, split)
        for _ in 0..<100 where selects(d).last != "writer:@1" { try? await Task.sleep(for: .milliseconds(20)) }
        #expect(selects(d).last == "writer:@1", "portenvd shows it too")
    }

    /// An agent starting while another is selected never moves the view
    /// (PLAN 4b): nothing is selected, and the terminal stays put.
    @Test func anAgentStartingWhileAnotherIsSelected() async {
        let (tabs, split, d) = make(active: "@1")
        await choose(.agent("lead"), tabs, split)
        let before = selects(d)
        let started = list(active: "lead:@1", adding: [TabInfo(id: "builder:@1", name: "builder", active: false, agentID: "builder")])
        d.setTabs(started)
        await receive(started, tabs, split)
        expectShows("lead:@1", tabs, split)
        #expect(selects(d) == before)
        // Nor with every tab or yours showing.
        await choose(.you, tabs, split)
        let before2 = selects(d)
        let another = list(active: "@1", adding: [TabInfo(id: "builder:@1", name: "builder", active: false, agentID: "builder"),
                                                   TabInfo(id: "sly:@1", name: "sly", active: false, agentID: "sly")])
        await receive(another, tabs, split)
        expectShows("@1", tabs, split)
        #expect(selects(d) == before2)
    }

    /// An agent with no tab yet: the terminal shows "Builder has no tab
    /// open", never another agent's pane, and ⌘W can't close the tab that
    /// isn't shown.
    @Test func anAgentWithNoTabShowsAnEmptyState() async {
        let (tabs, split, d) = make(active: "lead:@1")
        await choose(.agent("builder"), tabs, split)
        #expect(tabs.terminalTab == nil)
        #expect(!split.agreesWithBar)
        #expect(tabs.shownTabs.isEmpty)
        #expect(TerminalLabel.noTab(tabs.selection, team: team) == "Builder has no tab open")
        #expect(await tabs.requestClose() == .nothing)
        #expect(!d.calls.contains { $0.count > 1 && $0[1] == "close-tab" })
        // Selecting it again once it has a tab shows that tab.
        await choose(.agent("lead"), tabs, split)
        expectShows("lead:@1", tabs, split)
    }

    /// The keyboard (⌘⇧], ⌃Tab, ⌘1–9) moves through the tabs the bar
    /// shows, never to a hidden one.
    @Test func tabKeysStayInTheBar() async {
        let (tabs, split, _) = make(active: "@1")
        await choose(.agent("writer"), tabs, split)
        await tabs.selectNext()
        split.tabsChanged()
        expectShows("writer:@2", tabs, split)
        await tabs.selectNext()
        split.tabsChanged()
        expectShows("writer:@1", tabs, split)
        await tabs.selectPrevious()
        split.tabsChanged()
        expectShows("writer:@2", tabs, split)
        await tabs.select(number: 1)
        split.tabsChanged()
        expectShows("writer:@1", tabs, split)
        await tabs.select(number: 3)
        expectShows("writer:@1", tabs, split)
    }

    /// A tab outside the filter becoming current by the owner's own action
    /// (a new tab, focusing a pane that shows another agent's tab) widens
    /// the selection to every tab, so the bar still marks it.
    @Test func aHiddenTabMadeCurrentWidensTheSelection() async {
        let (tabs, split, _) = make(active: "lead:@1")
        var widened: [TeamSelection] = []
        tabs.selectionWidened = { widened.append($0) }
        // Two panes: Lead's tab in one, the owner's in the other, then Code
        // Writer selected (it shows in the focused pane).
        await split.split(.right)
        await split.settled()
        split.tabsChanged()
        #expect(split.layout.pane(showing: "lead:@1") != nil)
        await choose(.agent("writer"), tabs, split)
        await split.settled()
        expectShows("writer:@1", tabs, split)
        // A click in the pane showing Lead's tab.
        let leadPane = split.layout.pane(showing: "lead:@1")
        if let leadPane {
            split.focus(leadPane)
            await split.settled()
            #expect(tabs.selection == .allTabs)
            #expect(widened == [.allTabs])
            expectShows("lead:@1", tabs, split)
        }
        // A new tab (the owner's) under Code Writer's filter.
        await choose(.agent("writer"), tabs, split)
        widened = []
        let made = await tabs.newTab()
        split.tabsChanged()
        await split.settled()
        #expect(tabs.selection == .allTabs && widened == [.allTabs])
        expectShows(made?.id ?? "", tabs, split)
    }

    /// VoiceOver (R-0314): the terminal names the same agent and tab as the
    /// sidebar's selection and the tab bar.
    @Test func theTerminalNamesTheSelectedAgent() async {
        let (tabs, split, _) = make(active: "@1")
        #expect(tabs.terminalTab.map { TerminalLabel.of($0, team: team) } == "Terminal: You \u{00B7} shell")
        await choose(.agent("lead"), tabs, split)
        let lead = tabs.terminalTab.map { TerminalLabel.of($0, team: team) }
        #expect(lead == "Terminal: Lead \u{00B7} lead")
        await choose(.agent("writer"), tabs, split)
        let writer = tabs.terminalTab.map { TerminalLabel.of($0, team: team) } ?? ""
        #expect(writer == "Terminal: Code Writer \u{00B7} writer")
        #expect(writer.contains(team[1].name) && !writer.contains("Lead"))
        // The same words as the tab the bar marks current.
        let barTitle = tabs.terminalTab.map { TabLabel.of($0, team: team) { _ in false }.title }
        #expect(writer == "Terminal: " + (barTitle ?? "-"))
    }
}
