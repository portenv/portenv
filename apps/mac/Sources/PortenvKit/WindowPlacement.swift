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
    public static func visible(_ frame: CGRect, screens: [CGRect]) -> CGRect {
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
}
