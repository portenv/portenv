// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The tab bar (PLAN.md 1.2, GUIDELINES.md §3, §4.1): one tab per tmux
/// window in the box, followed as the box agent reports them; the app owns
/// names and order.
@MainActor
struct TabsTests {
    func waitUntil(_ cond: @MainActor () -> Bool) async {
        for _ in 0..<100 where !cond() { try? await Task.sleep(for: .milliseconds(20)) }
    }

    func names(_ c: TabsController) -> String {
        c.tabs.map { $0.name + ($0.active ? "*" : "") }.joined(separator: ",")
    }

    func tabCalls(_ d: FakeDaemon) -> [[String]] {
        d.calls.filter { $0.count > 1 && $0[1].hasSuffix("tab") }
    }

    @Test func theTabsFollowWhatTheBoxReports() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        let following = Task { await c.follow() }
        defer { following.cancel() }
        await waitUntil { names(c) == "shell*" }
        #expect(names(c) == "shell*")
        // A window something in the box opened itself shows as a tab.
        d.pushTabs([TabInfo(id: "@0", name: "shell", active: false), TabInfo(id: "@4", name: "server", active: true)])
        await waitUntil { names(c) == "shell,server*" }
        #expect(names(c) == "shell,server*")
    }

    @Test func whenTheWatchEndsTheTabsAreWatchedAgain() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d, retry: .milliseconds(30))
        let following = Task { await c.follow() }
        defer { following.cancel() }
        await waitUntil { names(c) == "shell*" }
        d.endTabWatches()
        await waitUntil { d.calls.filter { $0 == ["app", "watch-tabs", "acme-api"] }.count >= 2 }
        #expect(d.calls.filter { $0 == ["app", "watch-tabs", "acme-api"] }.count >= 2)
    }

    @Test func newTabsAreNamedShellThenNumbered() {
        #expect(TabNames.next(after: []) == "shell")
        #expect(TabNames.next(after: ["claude"]) == "shell")
        #expect(TabNames.next(after: ["shell"]) == "shell 2")
        #expect(TabNames.next(after: ["shell", "shell 2", "build"]) == "shell 3")
        #expect(TabNames.next(after: ["shell", "shell 3"]) == "shell 2")
    }

    @Test func plusOpensANewTabAndShowsIt() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        await c.newTab()
        #expect(tabCalls(d).contains(["app", "new-tab", "acme-api", "shell 2"]))
        #expect(names(c) == "shell,shell 2*", "shown at once, without waiting for the watch")
    }

    @Test func theLastTabCantBeClosed() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        #expect(!c.canClose)
        await c.close("@0")
        #expect(!tabCalls(d).contains { $0[1] == "close-tab" })
        await c.newTab()
        #expect(c.canClose)
        let id = c.tabs[1].id
        await c.close(id)
        #expect(tabCalls(d).contains(["app", "close-tab", "acme-api", id]))
        #expect(names(c) == "shell*")
    }

    @Test func renamesAreCheckedBeforeTheyGo() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        for bad in ["", "   ", String(repeating: "x", count: 33), "a\tb"] {
            #expect(await c.rename("@0", to: bad) == false)
            #expect(c.error != nil)
            c.dismissError()
        }
        #expect(!tabCalls(d).contains { $0[1] == "rename-tab" })
        #expect(await c.rename("@0", to: "  server  "))
        #expect(tabCalls(d).contains(["app", "rename-tab", "acme-api", "@0", "server"]), "trimmed")
        #expect(names(c) == "server*")
    }

    @Test func selectingShowsTheTabAtOnce() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        await c.newTab()
        await c.select("@0")
        #expect(tabCalls(d).contains(["app", "select-tab", "acme-api", "@0"]))
        #expect(names(c) == "shell*,shell 2")
    }

    func threeTabs(_ d: FakeDaemon) {
        d.pushTabs([TabInfo(id: "@0", name: "claude", active: false),
                    TabInfo(id: "@1", name: "shell", active: true),
                    TabInfo(id: "@2", name: "build", active: false)])
    }

    /// ⌘1–⌘9 choose a tab by position; a number past the last tab does
    /// nothing.
    @Test func commandNumberChoosesATab() async {
        let d = FakeDaemon()
        threeTabs(d)
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        await c.select(number: 1)
        #expect(names(c) == "claude*,shell,build")
        await c.select(number: 3)
        #expect(names(c) == "claude,shell,build*")
        await c.select(number: 1)
        await c.select(number: 9)
        #expect(names(c) == "claude*,shell,build", "⌘9 with three tabs does nothing")
        #expect(!tabCalls(d).contains(["app", "select-tab", "acme-api", ""]))
    }

    /// ⌘⇧[ and ⌘⇧] go to the previous and next tab, wrapping around.
    @Test func previousAndNextWrapAround() async {
        let d = FakeDaemon()
        threeTabs(d)
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        await c.selectNext()
        #expect(names(c) == "claude,shell,build*")
        await c.selectNext()
        #expect(names(c) == "claude*,shell,build")
        await c.selectPrevious()
        #expect(names(c) == "claude,shell,build*")
        await c.selectPrevious()
        #expect(names(c) == "claude,shell*,build")
    }

    /// ⌘W on a tab with nothing running closes it at once.
    @Test func closingAnIdleTabDoesntAsk() async {
        let d = FakeDaemon()
        threeTabs(d)
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        #expect(await c.requestClose() == .closed)
        #expect(tabCalls(d).contains(["app", "close-tab", "acme-api", "@1"]))
        #expect(c.pendingClose == nil)
    }

    /// Closing a tab with a running program asks first, in the owner's
    /// words, and the program comes from the box agent at that moment (not
    /// from a stale list): nothing closes until the person confirms.
    @Test func closingABusyTabAsksFirst() async {
        let d = FakeDaemon()
        threeTabs(d)
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        // The program started after the last update the app saw.
        d.setTabs([TabInfo(id: "@0", name: "claude", active: false),
                   TabInfo(id: "@1", name: "shell", active: true, program: "npm"),
                   TabInfo(id: "@2", name: "build", active: false)])
        #expect(await c.requestClose() == .asking)
        #expect(c.pendingClose?.program == "npm")
        #expect(c.pendingClose.map(TabText.closeQuestion) == "Close this tab? npm is still running.")
        #expect(!tabCalls(d).contains { $0[1] == "close-tab" })
        c.cancelClose()
        #expect(c.pendingClose == nil)
        try? await Task.sleep(for: .milliseconds(200)) // nothing closes later either
        #expect(!tabCalls(d).contains { $0[1] == "close-tab" })
        #expect(await c.requestClose(id: "@1") == .asking)
        await c.confirmClose(c.pendingClose!)
        #expect(tabCalls(d).contains(["app", "close-tab", "acme-api", "@1"]))
        #expect(c.pendingClose == nil)
    }

    /// A portenvd older than the app (it lacks a call, gRPC's
    /// "unimplemented") gets a plain line saying what to do (§10), never the
    /// raw "unknown method NewTab for service …". (Found at the Mac,
    /// 2026-10-10, with the login item still on an older build.)
    @Test func anOlderPortenvdSaysWhatToDo() {
        let e = DaemonError.fromRPC(message: "unknown method NewTab for service portenv.daemon.v1.DaemonService",
                                    unimplemented: true, unavailable: false)
        #expect(e.message == DaemonError.outOfDate)
        #expect(!e.message.contains("unknown method"))
        #expect(!e.unavailable)
        // Other failures keep portenvd's own plain message.
        #expect(DaemonError.fromRPC(message: "no box named demo here", unimplemented: false, unavailable: false).message
                == "no box named demo here")
        #expect(DaemonError.fromRPC(message: "connection refused", unimplemented: false, unavailable: true).unavailable)
    }

    /// ⌃Tab and ⌃⇧Tab move to the next and previous tab on any keyboard
    /// layout (⌘⇧[ / ⌘⇧] need a bracket key, which AZERTY lacks), and
    /// nothing else does: plain Tab, ⌥Tab and ⌘Tab stay with the terminal
    /// and the system. (Found at the Mac, 2026-10-10.)
    @Test func controlTabStepsThroughTabsOnAnyLayout() {
        #expect(TabKeys.step(keyCode: 48, control: true, shift: false, option: false, command: false) == 1)
        #expect(TabKeys.step(keyCode: 48, control: true, shift: true, option: false, command: false) == -1)
        #expect(TabKeys.step(keyCode: 48, control: false, shift: false, option: false, command: false) == nil)
        #expect(TabKeys.step(keyCode: 48, control: false, shift: true, option: false, command: false) == nil)
        #expect(TabKeys.step(keyCode: 48, control: true, shift: false, option: true, command: false) == nil)
        #expect(TabKeys.step(keyCode: 48, control: true, shift: false, option: false, command: true) == nil)
        #expect(TabKeys.step(keyCode: 0, control: true, shift: false, option: false, command: false) == nil)
    }

    /// Close Tab closes the tab even though SwiftUI dismisses the alert
    /// (clearing the pending close) before the button's task runs: the
    /// question's own close is passed to the button, never read back
    /// afterwards. (Found at the Mac, 2026-10-10: Close Tab did nothing.)
    @Test func closeTabStillClosesAfterTheAlertIsDismissed() async {
        let d = FakeDaemon()
        threeTabs(d)
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        d.setTabs([TabInfo(id: "@0", name: "claude", active: false),
                   TabInfo(id: "@1", name: "shell", active: true, program: "sleep"),
                   TabInfo(id: "@2", name: "build", active: false)])
        #expect(await c.requestClose() == .asking)
        let asked = c.pendingClose!
        c.cancelClose() // what the alert's dismissal does, first
        await c.confirmClose(asked) // then the Close Tab button's task
        #expect(tabCalls(d).contains(["app", "close-tab", "acme-api", "@1"]))
        #expect(c.pendingClose == nil)
    }

    /// ⌘W on the last tab closes the window (which saves and releases the
    /// box), like Terminal and Safari; the tab itself is never closed.
    @Test func closingTheLastTabClosesTheWindow() async {
        let d = FakeDaemon()
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        #expect(await c.requestClose() == .closeWindow)
        #expect(!tabCalls(d).contains { $0[1] == "close-tab" })
    }

    @Test func aFailureIsSaidPlainly() async {
        let d = FakeDaemon()
        d.failing["new-tab"] = "the box agent is unavailable (its channel is down or failed verification); restart the box"
        let c = TabsController(box: "acme-api", daemon: d)
        await c.load()
        await c.newTab()
        #expect(c.error == "the box agent is unavailable (its channel is down or failed verification); restart the box")
        #expect(names(c) == "shell*")
    }
}
