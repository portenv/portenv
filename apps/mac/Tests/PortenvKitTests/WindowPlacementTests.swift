// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Testing

@testable import PortenvKit

/// A window restored from a display that isn't there any more comes back on
/// a screen you can see (R-0044): a first-run alert must never open where
/// nobody sees it.
struct WindowPlacementTests {
    // A laptop's screen (menu bar on it) and an external display above-right
    // of it, as AppKit gives them (origin bottom-left, visible frames).
    private let laptop = CGRect(x: 0, y: 0, width: 1470, height: 919)
    private let external = CGRect(x: 1470, y: -484, width: 2560, height: 1410)

    @Test func aFrameOnAScreenStaysPut() {
        let onExternal = CGRect(x: 2399, y: 275, width: 900, height: 472)
        #expect(WindowPlacement.visible(onExternal, screens: [laptop, external]) == onExternal)
        let onLaptop = CGRect(x: 100, y: 100, width: 900, height: 472)
        #expect(WindowPlacement.visible(onLaptop, screens: [laptop]) == onLaptop)
        // Partly off the edge, title bar still reachable: left alone.
        let hanging = CGRect(x: 1300, y: 300, width: 900, height: 472)
        #expect(WindowPlacement.visible(hanging, screens: [laptop]) == hanging)
    }

    @Test func aFrameFromAMissingDisplayIsCentredOnTheMainScreen() {
        let fromExternal = CGRect(x: 2399, y: 275, width: 900, height: 472)
        let placed = WindowPlacement.visible(fromExternal, screens: [laptop])
        #expect(placed.size == fromExternal.size)
        #expect(placed.midX == laptop.midX)
        #expect(placed.midY == laptop.midY)
    }

    /// Only the title bar matters: a window whose title bar is off every
    /// screen can't be dragged back.
    @Test func aTitleBarOffScreenIsNotVisible() {
        let titleAbove = CGRect(x: 100, y: 900, width: 900, height: 472) // top at 1372, screen ends at 919
        let placed = WindowPlacement.visible(titleAbove, screens: [laptop])
        #expect(placed.maxY <= laptop.maxY)
        #expect(placed.minY >= laptop.minY)
    }

    @Test func aWindowLargerThanTheScreenIsShrunkToFit() {
        let huge = CGRect(x: 5000, y: 5000, width: 3000, height: 2000)
        let placed = WindowPlacement.visible(huge, screens: [laptop])
        #expect(laptop.contains(placed))
    }

    @Test func noScreensLeavesTheFrameAlone() {
        let frame = CGRect(x: 5000, y: 5000, width: 900, height: 472)
        #expect(WindowPlacement.visible(frame, screens: []) == frame)
    }

    // MARK: First run's window (R-0186)

    /// The board's size, centred on the display under the pointer at launch,
    /// never the other one, and never full screen.
    @Test func firstRunOpensAtTheBoardsSizeOnTheLaunchDisplay() {
        let onExternal = WindowPlacement.firstRun(pointer: CGPoint(x: 3000, y: 200), screens: [laptop, external])
        #expect(onExternal == CGRect(x: external.midX - 360, y: external.midY - 260, width: 720, height: 520))
        let onLaptop = WindowPlacement.firstRun(pointer: CGPoint(x: 200, y: 200), screens: [laptop, external])
        #expect(onLaptop == CGRect(x: 375, y: 200, width: 720, height: 520))
    }

    /// The pointer on no screen: the main one. A screen smaller than the
    /// board: the window fits it.
    @Test func firstRunFallsBackToTheMainScreenAndFits() {
        let lost = WindowPlacement.firstRun(pointer: CGPoint(x: -9000, y: 0), screens: [laptop, external])
        #expect(lost?.midX == laptop.midX && lost?.width == 720)
        let small = CGRect(x: 0, y: 0, width: 600, height: 400)
        #expect(WindowPlacement.firstRun(pointer: .zero, screens: [small]) == small)
        #expect(WindowPlacement.firstRun(pointer: .zero, screens: []) == nil)
    }

    /// Only first run at launch places its window (R-0186). New Workspace…
    /// from the list keeps the window as it is: in a workspace tab it once
    /// resized the whole tab group to the board's 720×520 (the owner's
    /// dev-build test). Never a tab, never full screen.
    @Test func onlyFirstRunAtLaunchPlacesItsWindow() {
        #expect(WindowPlacement.placesFirstRun(fromBoxList: false, tabbed: false, fullScreen: false))
        #expect(!WindowPlacement.placesFirstRun(fromBoxList: true, tabbed: false, fullScreen: false))
        #expect(!WindowPlacement.placesFirstRun(fromBoxList: true, tabbed: true, fullScreen: false))
        #expect(!WindowPlacement.placesFirstRun(fromBoxList: false, tabbed: true, fullScreen: false))
        #expect(!WindowPlacement.placesFirstRun(fromBoxList: false, tabbed: false, fullScreen: true))
    }

    /// CC-0500: a window full screen on the external display fills it
    /// (2560 x 1440 at y -484), so its title strip (y 928 to 956) is above
    /// that display's visible frame (it ends at 926, under its menu bar).
    /// The title-bar check alone calls it off screen and shrinks it to the
    /// laptop's visible size, 57% of the display: the owner's "drawn at
    /// ~58% in the top-left" after swiping Spaces. Full screen, it stays.
    @Test func aFullScreenWindowIsNeverMoved() {
        let fullScreenOnExternal = CGRect(x: 1470, y: -484, width: 2560, height: 1440)
        let moved = WindowPlacement.visible(fullScreenOnExternal, screens: [laptop, external])
        #expect(moved.size == laptop.size) // what happened before the fix
        #expect(WindowPlacement.visible(fullScreenOnExternal, screens: [laptop, external], fullScreen: true)
            == fullScreenOnExternal)
        // On the laptop: the notch's menu bar strip is outside the visible frame too.
        let fullScreenOnLaptop = CGRect(x: 0, y: 0, width: 1470, height: 956)
        #expect(WindowPlacement.visible(fullScreenOnLaptop, screens: [laptop, external]) != fullScreenOnLaptop)
        #expect(WindowPlacement.visible(fullScreenOnLaptop, screens: [laptop, external], fullScreen: true)
            == fullScreenOnLaptop)
        // With no screens known, still left alone.
        #expect(WindowPlacement.visible(fullScreenOnLaptop, screens: [], fullScreen: true) == fullScreenOnLaptop)
    }
}
