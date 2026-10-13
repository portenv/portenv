// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm

/// A terminal view's colours, in one place for every terminal the app shows
/// (the main window's and each split-view pane's): the system's text
/// background and text colours, the light or dark ANSI palette, and the
/// accent for the caret and selection (the owner, 2026-10-11; GUIDELINES
/// §4.5). Resolved for the appearance given, since a dynamic colour would
/// follow whatever appearance draws next.
@MainActor
public enum TerminalColours {
    public static func apply(to terminal: TerminalView, for appearance: NSAppearance) {
        let system = TerminalPalette.systemColours(appearance)
        terminal.nativeBackgroundColor = NSColor(system.background)
        terminal.nativeForegroundColor = NSColor(system.foreground)
        terminal.caretColor = NSColor(system.caret)
        terminal.selectedTextBackgroundColor = NSColor(system.selection)
        // installColors also drops the terminal's cached colours and redraws.
        terminal.installColors(TerminalPalette.matching(appearance).ansi.map { SwiftTerm.Color($0) })
        terminal.needsDisplay = true
    }
}

public extension NSColor {
    convenience init(_ c: RGB) {
        self.init(srgbRed: CGFloat(c.red) / 255, green: CGFloat(c.green) / 255, blue: CGFloat(c.blue) / 255, alpha: 1)
    }
}

public extension SwiftTerm.Color {
    convenience init(_ c: RGB) {
        self.init(red8: UInt16(c.red), green8: UInt16(c.green), blue8: UInt16(c.blue))
    }
}
