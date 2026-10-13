// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm

/// Makes every terminal view the app shows, each with its guard (R-0134,
/// R-0140). Nothing else in the app constructs one;
/// `scripts/check-terminal-views.sh` fails the build on a terminal view made
/// anywhere else. It lives here, below the app and above PortenvKit, so it
/// can make both today's attached terminal and the per-tab view.
@MainActor
public enum TerminalViews {
    /// Today's attached terminal (`portenv attach` in a process), guarded.
    /// Keep the guard: the view holds its delegate weakly.
    public static func attached() -> (view: LocalProcessTerminalView, guard: TerminalGuard) {
        let view = LocalProcessTerminalView(frame: .zero)
        let guardian = TerminalGuard(for: view)
        view.terminalDelegate = guardian
        return (view, guardian)
    }

    /// One tab's view (ADR 0020): it drops the emulator's replies, a
    /// program's clipboard, link and title requests, and (in 1.2c) puts the
    /// default-deny filter in front of everything it's fed.
    public static func tab() -> MarkedTerminalView {
        MarkedTerminalView(frame: .zero)
    }
}
