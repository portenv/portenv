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
}
