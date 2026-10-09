// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// docs/design/GUIDELINES.md §3.1: every row of the state-line table, with
/// its symbol, in the user's 12/24-hour setting.
struct StateLineTableTests {
    let tz = TimeZone(identifier: "Europe/Paris")!
    let h24 = Locale(identifier: "en_GB")
    let h12 = Locale(identifier: "en_US")
    // 14:58 in Paris.
    let at = ISO8601DateFormatter().date(from: "2026-10-08T12:58:00Z")!
    var later: Date { at.addingTimeInterval(3600) }

    func line(_ s: BoxState, now: Date? = nil, locale: Locale? = nil) -> String {
        s.line(now: now ?? later, timeZone: tz, locale: locale ?? h24)
    }

    @Test func newBoxNeverSaved() {
        let s = BoxState(save: .notSavedYet)
        #expect(line(s) == "This Mac · Not saved yet")
        #expect(s.symbol == nil)
    }

    @Test func saving() {
        let s = BoxState(save: .saving, savedAt: at)
        #expect(line(s) == "This Mac · Saving…")
        #expect(s.symbol == "arrow.triangle.2.circlepath")
        #expect(s.symbolSpins)
    }

    @Test func saved() {
        let s = BoxState(save: .saved, savedAt: at)
        #expect(line(s) == "This Mac · Saved at 14:58")
        #expect(s.symbol == "checkmark.circle")
    }

    @Test func savedJustNowForTheFirstMinute() {
        let s = BoxState(save: .saved, savedAt: at)
        #expect(line(s, now: at.addingTimeInterval(59)) == "This Mac · Saved just now")
        #expect(line(s, now: at.addingTimeInterval(61)) == "This Mac · Saved at 14:58")
    }

    @Test func timesFollowThe12Or24HourSetting() {
        let s = BoxState(save: .saved, savedAt: at)
        #expect(line(s, locale: h24) == "This Mac · Saved at 14:58")
        #expect(line(s, locale: h12).replacingOccurrences(of: "\u{202F}", with: " ") == "This Mac · Saved at 2:58 PM")
    }

    @Test func offline() {
        let s = BoxState(save: .offline, savedAt: at)
        #expect(line(s) == "This Mac · Offline · will save later")
        #expect(s.symbol == "icloud.slash")
    }

    @Test func openOnAServerByName() {
        let s = BoxState(save: .saved, savedAt: at, location: "portenv@test.example.net")
        #expect(line(s) == "test.example.net · Saved at 14:58")
        #expect(s.symbol == "checkmark.circle")
    }

    @Test func boxAgentUnavailable() {
        let s = BoxState(save: .agentUnavailable, savedAt: at)
        #expect(line(s) == "This Mac · Not saved since 14:58")
        #expect(s.symbol == "exclamationmark.triangle")
        #expect(BoxState(save: .agentUnavailable).symbol == "exclamationmark.triangle")
    }

    @Test func retryingAndFailedSaves() {
        #expect(line(BoxState(save: .retrying, savedAt: at)) == "This Mac · Not saved since 14:58 · retrying")
        #expect(line(BoxState(save: .notSaved, savedAt: at)) == "This Mac · Not saved since 14:58")
        #expect(line(BoxState(save: .quitUnsaved, savedAt: at)) == "This Mac · Not saved since 14:58 · Portenv quit before saving")
        #expect(BoxState(save: .quitUnsaved).symbol == "exclamationmark.triangle")
    }

    @Test func movingSaysWhere() {
        #expect(StateLine.moving(to: "portenv@test.example.net") == "Moving to test.example.net…")
        #expect(StateLine.moving(to: "this-mac") == "Moving to This Mac…")
    }

    @Test func justReverted() {
        #expect(StateLine.reverted(to: at, timeZone: tz, locale: h24) == "Reverted to 14:58 · your changes were kept")
        #expect(StateLine.transientSeconds == 5)
    }

    @Test func spinningRespectsReduceMotion() {
        #expect(BoxState(save: .saving).symbolSpins)
        #expect(!BoxState(save: .saved).symbolSpins)
    }
}

/// §2: servers by name (default: the host name without the user), never
/// user@IP.
struct ServerNameTests {
    @Test func theHostWithoutTheUser() {
        #expect(ServerName.display("portenv@52.47.207.191") == "52.47.207.191")
        #expect(ServerName.display("ubuntu@build.example.net") == "build.example.net")
        #expect(ServerName.display("build.example.net") == "build.example.net")
        #expect(ServerName.display("this-mac") == "This Mac")
    }
}

/// §3.2: the title menu and the Box menu contain the same items, in the same
/// order.
struct BoxMenuOrderTests {
    func titles(_ items: [BoxMenuItem]) -> [String] { items.map(\.title) }

    @Test func theGuidelineOrder() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false,
                                       location: "this-mac", servers: ["portenv@52.47.207.191"])
        #expect(titles(items) == ["Rename…", "Move To", "—", "Revert To", "Make a Save Point", "—", "Show in Finder"])
        let move = items[1].submenu
        #expect(titles(move) == ["This Mac", "—", "52.47.207.191", "—", "Add a Server…"])
        #expect(move[0].checked && !move[2].checked)
        #expect(move[0].enabled, "the current location is checked, not disabled")
        #expect(titles(items[3].submenu) == ["Last Save Point", "Browse All Saves…"])
        #expect(items[4].shortcut == "⌘S")
        #expect(items[6].shortcut == "⌥⌘R")
    }

    @Test func restartBoxOnlyWhenTheAgentIsUnavailableOrWithOption() {
        let normal = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [])
        #expect(!titles(normal).contains("Restart Box"))
        let down = BoxMenuModel.items(open: true, busy: false, agentUnavailable: true, optionHeld: false, location: "this-mac", servers: [])
        #expect(titles(down) == ["Rename…", "Move To", "—", "Revert To", "Make a Save Point", "—", "Restart Box", "Show in Finder"])
        let option = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: true, location: "this-mac", servers: [])
        #expect(titles(option).contains("Restart Box"))
    }

    @Test func whatCantWorkYetIsDisabled() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [])
        let byTitle = Dictionary(items.map { ($0.title, $0) }, uniquingKeysWith: { a, _ in a })
        #expect(byTitle["Rename…"]?.enabled == false)
        #expect(byTitle["Show in Finder"]?.enabled == false)
        #expect(items[1].submenu.last?.enabled == false) // Add a Server…
        #expect(items[3].submenu.last?.enabled == false) // Browse All Saves…
        #expect(byTitle["Make a Save Point"]?.enabled == true)
    }
}

/// §4.5: the terminal's text never touches the window's edge.
struct TerminalLayoutTests {
    @Test func anEightPointMargin() {
        #expect(TerminalLayout.margin == 8)
    }
}

/// §12: every control says what it is or does.
struct AccessibilityLabelTests {
    @Test func theTitleAndTheSyncSymbol() {
        #expect(A11y.titleLabel(box: "acme-api") == "acme-api, box menu")
        #expect(A11y.symbolLabel(BoxState(save: .saved)) == "Saved")
        #expect(A11y.symbolLabel(BoxState(save: .saving)) == "Saving")
        #expect(A11y.symbolLabel(BoxState(save: .offline)) == "Offline")
        #expect(A11y.symbolLabel(BoxState(save: .agentUnavailable)) == "Not saved")
        #expect(A11y.terminal(box: "acme-api") == "Terminal in acme-api")
    }
}

/// The symbol and the text always come from the same state: the symbol
/// spins only while the line says something is in progress (a rotation once
/// kept going next to "Saved just now").
@MainActor
struct TitleLookTests {
    @Test func everyStateAgrees() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli)
        #expect(c.title.consistent, "opening: \(c.title)")
        await c.open()
        for json in [
            #"{"state":"SAVE_STATE_NOT_SAVED_YET"}"#,
            #"{"state":"SAVE_STATE_SAVING","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_OFFLINE","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_AGENT_UNAVAILABLE","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_RETRYING","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_NOT_SAVED","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_QUIT_UNSAVED","saved_at":"2026-10-08T12:58:00Z"}"#,
            #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z","location":"portenv@test.example.net"}"#,
        ] {
            cli.state = json
            await c.refresh()
            #expect(c.title.consistent, "\(json) → \(c.title)")
        }
    }

    @Test func savedNeverSpins() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli)
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_SAVING"}"#
        await c.refresh()
        #expect(c.title.spins)
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z"}"#
        await c.refresh()
        #expect(!c.title.spins && c.title.symbol == "checkmark.circle")
    }
}

/// PLAN.md 1.4: a package that can't be installed shows in the inspector,
/// never in the state line.
struct FailedPackagesTests {
    @Test func theInspectorLine() {
        #expect(InspectorText.packages([]) == nil)
        #expect(InspectorText.packages(["tree"]) == "1 package couldn't be installed: tree · Retry")
        #expect(InspectorText.packages(["tree", "jq"]) == "2 packages couldn't be installed: tree, jq · Retry")
    }

    @Test func theStateLineStaysNormal() {
        let at = ISO8601DateFormatter().date(from: "2026-10-08T12:58:00Z")!
        let s = BoxState.parse(#"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-08T12:58:00Z","failed_packages":["no-such-package"]}"#)
        #expect(s?.failedPackages == ["no-such-package"])
        #expect(s?.line(now: at.addingTimeInterval(3600), timeZone: TimeZone(identifier: "Europe/Paris")!, locale: Locale(identifier: "en_GB")) == "This Mac · Saved at 14:58")
    }
}
