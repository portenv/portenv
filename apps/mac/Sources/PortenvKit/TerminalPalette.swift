// SPDX-License-Identifier: Apache-2.0

import AppKit

/// An sRGB colour, 8 bits a channel.
public struct RGB: Equatable, Sendable {
    public let red: UInt8
    public let green: UInt8
    public let blue: UInt8

    /// From 0xRRGGBB.
    public init(_ hex: UInt32) {
        red = UInt8((hex >> 16) & 0xFF)
        green = UInt8((hex >> 8) & 0xFF)
        blue = UInt8(hex & 0xFF)
    }

    /// From an NSColor, as it is in sRGB (resolve dynamic colours first,
    /// under the appearance they're for).
    public init(_ color: NSColor) {
        let c = color.usingColorSpace(.sRGB) ?? .black
        func byte(_ v: CGFloat) -> UInt8 { UInt8((min(max(v, 0), 1) * 255).rounded()) }
        red = byte(c.redComponent)
        green = byte(c.greenComponent)
        blue = byte(c.blueComponent)
    }
}

/// WCAG 2 contrast (GUIDELINES §12: at least 4.5:1 for text).
public enum Contrast {
    /// Relative luminance, 0 (black) to 1 (white).
    public static func luminance(_ c: RGB) -> Double {
        func linear(_ v: UInt8) -> Double {
            let s = Double(v) / 255
            return s <= 0.04045 ? s / 12.92 : pow((s + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * linear(c.red) + 0.7152 * linear(c.green) + 0.0722 * linear(c.blue)
    }

    /// From 1:1 (the same) to 21:1 (black on white), either way round.
    public static func ratio(_ a: RGB, _ b: RGB) -> Double {
        let la = luminance(a), lb = luminance(b)
        return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
    }
}

/// The terminal's colours when it matches the system (the owner,
/// 2026-10-11), as Terminal.app's Basic profile does: the system's text
/// background and text colours, and one ANSI palette for light and one for
/// dark. The light palette uses darker greens, yellows and cyans, so each
/// normal colour is readable (4.5:1) on its background. "Black" on dark and
/// "white" on light are greys for the same reason. Bright black is the
/// colour shells and tools use for dim text (autosuggestions, comments), so
/// it sits between black and the background, as Terminal.app's grey does.
public struct TerminalPalette: Sendable {
    public let name: String
    /// A sample of the system's text background, for the contrast tests
    /// (the terminal itself uses NSColor.textBackgroundColor).
    public let background: RGB
    public let foreground: RGB
    /// The 16 ANSI colours: black, red, green, yellow, blue, magenta, cyan,
    /// white, then their bright variants.
    public let ansi: [RGB]

    public static let light = TerminalPalette(
        name: "light", background: RGB(0xFFFFFF), foreground: RGB(0x000000),
        ansi: ([0x000000, 0xC4251C, 0x1A7F37, 0x8A6100, 0x0A56C2, 0xA42AA2, 0x0B7A85, 0x636B74,
               0x6E7781, 0xD1242F, 0x197A35, 0x7D5800, 0x0969DA, 0xB12FAE, 0x087380, 0x57606A] as [UInt32]).map { RGB($0) }
    )

    public static let dark = TerminalPalette(
        name: "dark", background: RGB(0x1E1E1E), foreground: RGB(0xFFFFFF),
        ansi: ([0x8C8C8C, 0xFF6B66, 0x5AD25A, 0xE5C24F, 0x6CB2FF, 0xE08AF0, 0x52CCDB, 0xD4D4D4,
               0xA6A6A6, 0xFF8C87, 0x7EE07E, 0xF2D872, 0x94C8FF, 0xEDA9F7, 0x7FDCE8, 0xFFFFFF] as [UInt32]).map { RGB($0) }
    )

    /// Whether an appearance is a dark one (Dark Aqua, or its high-contrast
    /// and vibrant forms).
    public static func isDark(_ appearance: NSAppearance) -> Bool {
        appearance.bestMatch(from: [.aqua, .darkAqua, .accessibilityHighContrastAqua, .accessibilityHighContrastDarkAqua,
                                    .vibrantLight, .vibrantDark])
            .map { [.darkAqua, .accessibilityHighContrastDarkAqua, .vibrantDark].contains($0) } ?? false
    }

    /// The palette for an appearance.
    public static func matching(_ appearance: NSAppearance) -> TerminalPalette { isDark(appearance) ? dark : light }

    /// The system's colours under an appearance, resolved (dynamic colours
    /// would otherwise follow whatever appearance is current).
    public struct System: Sendable {
        public let background: RGB
        public let foreground: RGB
        /// The caret: the system accent.
        public let caret: RGB
        /// Selected text: the system's selection colour, which follows the
        /// accent (or the Highlight colour the person chose).
        public let selection: RGB
    }

    public static func systemColours(_ appearance: NSAppearance) -> System {
        var s = System(background: light.background, foreground: light.foreground, caret: RGB(0x007AFF), selection: RGB(0xB3D7FF))
        appearance.performAsCurrentDrawingAppearance {
            s = System(background: RGB(NSColor.textBackgroundColor), foreground: RGB(NSColor.textColor),
                       caret: RGB(NSColor.controlAccentColor), selection: RGB(NSColor.selectedTextBackgroundColor))
        }
        return s
    }
}
