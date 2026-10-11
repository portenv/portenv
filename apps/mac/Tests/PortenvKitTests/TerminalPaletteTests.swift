// SPDX-License-Identifier: Apache-2.0

import AppKit
import Testing

@testable import PortenvKit

/// The terminal matches the system appearance (the owner, 2026-10-11): the
/// system's text colours, and an ANSI palette for light and one for dark
/// whose colours are readable on their background (GUIDELINES §12: 4.5:1).
struct TerminalPaletteTests {
    @Test func contrastIsWCAGs() {
        #expect(abs(Contrast.ratio(RGB(0x000000), RGB(0xFFFFFF)) - 21) < 0.01)
        #expect(abs(Contrast.ratio(RGB(0xFFFFFF), RGB(0x000000)) - 21) < 0.01)
        #expect(abs(Contrast.ratio(RGB(0x777777), RGB(0x777777)) - 1) < 0.01)
        // #767676 is the lightest grey that passes 4.5:1 on white.
        #expect(Contrast.ratio(RGB(0x767676), RGB(0xFFFFFF)) >= 4.5)
        #expect(Contrast.ratio(RGB(0x777777), RGB(0xFFFFFF)) < 4.5)
    }

    @Test(arguments: [TerminalPalette.light, TerminalPalette.dark])
    func eachNormalColourIsReadableOnItsBackground(_ p: TerminalPalette) {
        #expect(p.ansi.count == 16)
        for (i, c) in p.ansi.prefix(8).enumerated() {
            #expect(Contrast.ratio(c, p.background) >= 4.5, "colour \(i) in \(p.name)")
        }
        #expect(Contrast.ratio(p.foreground, p.background) >= 4.5)
    }

    @Test(arguments: [(NSAppearance.Name.aqua, false), (.darkAqua, true)])
    func thePaletteMatchesTheSystemsOwnBackground(_ name: NSAppearance.Name, _ dark: Bool) {
        // The real background is the system's text background: each normal
        // colour must be readable on it, not only on the palette's sample.
        let appearance = NSAppearance(named: name)!
        #expect(TerminalPalette.isDark(appearance) == dark)
        let p = TerminalPalette.matching(appearance)
        #expect(p.name == (dark ? "dark" : "light"))
        let system = TerminalPalette.systemColours(appearance)
        for (i, c) in p.ansi.prefix(8).enumerated() {
            #expect(Contrast.ratio(c, system.background) >= 4.5, "colour \(i) on \(name.rawValue)")
        }
        #expect(Contrast.ratio(system.foreground, system.background) >= 4.5)
    }

    @Test func theLightPaletteUsesDarkerColours() {
        // Green, yellow and cyan are the colours that wash out on white.
        for i in [2, 3, 6] {
            #expect(Contrast.luminance(TerminalPalette.light.ansi[i]) < Contrast.luminance(TerminalPalette.dark.ansi[i]))
        }
    }
}
