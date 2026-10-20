// SPDX-License-Identifier: Apache-2.0

import CoreGraphics

/// Where a restored window goes (R-0044). AppKit restores a window's saved
/// frame even when the display it was on has gone; the window then opens
/// where nobody can see it, and so does any alert on it.
public enum WindowPlacement {
    /// How much of the title bar must be on a screen for the window to count
    /// as reachable (it can be dragged back from there).
    static let titleBar: CGFloat = 28
    static let reachableWidth: CGFloat = 80

    /// The frame unchanged when its title bar is on one of `screens` (visible
    /// frames, the main screen first); otherwise the same size, shrunk to fit
    /// if need be, centred on the main screen.
    ///
    /// A full-screen window is always left alone (CC-0500): it fills its
    /// display, so its top edge lies in the menu bar's strip, outside every
    /// visible frame, and it would count as off screen. Moving it there
    /// (its window becomes key again when you swipe back to its Space)
    /// shrinks it to the main screen's size inside its full-screen Space:
    /// drawn small in the top-left, the rest black.
    public static func visible(_ frame: CGRect, screens: [CGRect], fullScreen: Bool = false) -> CGRect {
        if fullScreen { return frame }
        guard let main = screens.first else { return frame }
        let title = CGRect(x: frame.minX, y: frame.maxY - titleBar, width: frame.width, height: titleBar)
        for screen in screens {
            let shown = title.intersection(screen)
            if !shown.isNull, shown.width >= reachableWidth, shown.height >= titleBar { return frame }
        }
        let width = min(frame.width, main.width)
        let height = min(frame.height, main.height)
        return CGRect(x: main.midX - width / 2, y: main.midY - height / 2, width: width, height: height)
    }

    /// First run's window: the board's size (R-0186), centred on the
    /// display the app was launched from, the one under the pointer then
    /// (R-0196's note: it once opened full screen on the other display).
    /// Shrunk to fit a small screen. `screens` are visible frames, the
    /// main screen first, which is used when the pointer is on none.
    public static let firstRunSize = CGSize(width: 720, height: 520)

    public static func firstRun(pointer: CGPoint, screens: [CGRect], size: CGSize = firstRunSize) -> CGRect? {
        guard let screen = screens.first(where: { $0.contains(pointer) }) ?? screens.first else { return nil }
        let width = min(size.width, screen.width)
        let height = min(size.height, screen.height)
        return CGRect(x: (screen.midX - width / 2).rounded(), y: (screen.midY - height / 2).rounded(), width: width, height: height)
    }
}
