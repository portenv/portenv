// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import Testing

/// View › Bigger and Smaller (R-0362): one point a step, from the default
/// (13 pt, also the smallest) to 36 pt, remembered per workspace.
@MainActor
struct TerminalTextSizeTests {
    func defaults() throws -> UserDefaults {
        try #require(UserDefaults(suiteName: "TerminalTextSizeTests-\(UUID().uuidString)"))
    }

    @Test func oneStepAtATimeBetweenTheDefaultAnd36() {
        #expect(TerminalTextSize.defaultPoints == 13)
        #expect(TerminalTextSize.minimum == TerminalTextSize.defaultPoints)
        #expect(TerminalTextSize.applying(.bigger, to: 13) == 14)
        #expect(TerminalTextSize.applying(.bigger, to: 35) == 36)
        #expect(TerminalTextSize.applying(.bigger, to: 36) == nil)
        #expect(TerminalTextSize.applying(.smaller, to: 14) == 13)
        // Never below the default (the owner, R-0362).
        #expect(TerminalTextSize.applying(.smaller, to: 13) == nil)
    }

    @Test func outOfRangeSizesClamp() {
        #expect(TerminalTextSize.clamp(9) == 13)
        #expect(TerminalTextSize.clamp(50) == 36)
        #expect(TerminalTextSize.clamp(14.4) == 14)
        #expect(TerminalTextSize.clamp(.nan) == 13)
        #expect(TerminalTextSize.applying(.smaller, to: 9) == nil)
    }

    @Test func aWorkspaceRemembersItsSize() throws {
        let d = try defaults()
        let one = TerminalTextSizeState(box: "web", defaults: d)
        #expect(one.points == 13)
        #expect(one.apply(.bigger))
        #expect(one.apply(.bigger))
        // The next terminal in the workspace (a new tab or pane, the next
        // launch) starts at it; another workspace doesn't.
        #expect(TerminalTextSizeState(box: "web", defaults: d).points == 15)
        #expect(TerminalTextSizeState(box: "api", defaults: d).points == 13)
        #expect(d.double(forKey: TerminalTextSize.key("web")) == 15)
        #expect(TerminalTextSize.key("web") == "terminalTextSize.web")
    }

    @Test func backToTheDefaultForgetsIt() throws {
        let d = try defaults()
        let s = TerminalTextSizeState(box: "web", defaults: d)
        s.apply(.bigger)
        s.apply(.smaller)
        #expect(d.object(forKey: TerminalTextSize.key("web")) == nil)
        #expect(TerminalTextSizeState(box: "web", defaults: d).points == 13)
    }

    @Test func aStoredSizeOutOfRangeIsClamped() throws {
        let d = try defaults()
        d.set(99.0, forKey: TerminalTextSize.key("web"))
        #expect(TerminalTextSizeState(box: "web", defaults: d).points == 36)
        d.set(4.0, forKey: TerminalTextSize.key("web"))
        #expect(TerminalTextSizeState(box: "web", defaults: d).points == 13)
    }

    @Test func atTheLimitsNothingChangesAndNothingIsSaid() throws {
        let d = try defaults()
        var said: [String] = []
        let s = TerminalTextSizeState(box: "web", defaults: d) { said.append($0) }
        #expect(!s.apply(.smaller))
        #expect(s.points == 13)
        #expect(said.isEmpty)
        for _ in 0 ..< 40 { s.apply(.bigger) }
        #expect(s.points == 36)
        #expect(said.count == 23)
        #expect(said.last == "Terminal text size 36 points")
        #expect(!s.apply(.bigger))
    }

    // MARK: The View menu

    @Test func biggerAndSmallerOnlyNoDefaultSize() {
        let items = TerminalTextSizeMenu.items(points: 14)
        #expect(items.map(\.title) == ["Bigger", "Smaller"])
        #expect(items.map(\.key) == ["+", "-"])
        #expect(items.map(\.accessibilityLabel) == ["Make the terminal text bigger", "Make the terminal text smaller"])
        #expect(items.map(\.isEnabled) == [true, true])
    }

    @Test func smallerIsDisabledAtTheDefaultAndBiggerAt36() {
        #expect(TerminalTextSizeMenu.items(points: 13).map(\.isEnabled) == [true, false])
        #expect(TerminalTextSizeMenu.items(points: 36).map(\.isEnabled) == [false, true])
        // No terminal focused: both disabled.
        #expect(TerminalTextSizeMenu.items(points: nil).map(\.isEnabled) == [false, false])
    }

    @Test func theStateDrivesTheMenu() throws {
        let s = TerminalTextSizeState(box: "web", defaults: try defaults())
        #expect(s.menu.map(\.isEnabled) == [true, false])
        s.apply(.bigger)
        #expect(s.menu.map(\.isEnabled) == [true, true])
    }

    // MARK: Key equivalents

    func step(_ chars: String, _ flags: NSEvent.ModifierFlags) -> TerminalTextSize.Step? {
        TerminalTextSize.step(charactersIgnoringModifiers: chars, modifiers: flags)
    }

    @Test func commandPlusEqualsAndShiftEqualsAreBigger() {
        #expect(step("+", [.command]) == .bigger)
        // ⌘= (AZERTY's = key, and US without Shift).
        #expect(step("=", [.command]) == .bigger)
        // ⌘⇧=: US gives "+", some layouts keep "=".
        #expect(step("+", [.command, .shift]) == .bigger)
        #expect(step("=", [.command, .shift]) == .bigger)
    }

    @Test func commandMinusIsSmaller() {
        #expect(step("-", [.command]) == .smaller)
    }

    @Test func nothingElseIsTaken() {
        // Without ⌘, with ⌥ or ⌃, ⌘0 (Show Sidebar) and ⌘⇧− go on as before.
        #expect(step("=", []) == nil)
        #expect(step("+", [.shift]) == nil)
        #expect(step("-", []) == nil)
        #expect(step("=", [.command, .option]) == nil)
        #expect(step("=", [.command, .control]) == nil)
        #expect(step("-", [.command, .shift]) == nil)
        #expect(step("0", [.command]) == nil)
        #expect(step("0", [.command, .option]) == nil)
    }
}
