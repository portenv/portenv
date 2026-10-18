// SPDX-License-Identifier: Apache-2.0

#if PORTENV_DEV_PREVIEWS
import AppKit
import PortenvKit
import PortenvTerm
import SwiftUI

/// `Portenv --blocks-preview` (lane builds only, R-0220): the command-blocks
/// board's session played through the real tab view, shown in a window, so
/// the blocks can be tried by hand (VoiceOver, ⌘↑/⌘↓, Find, the sticky
/// header) before lane A's control-mode provider puts them in the main
/// window. Compiled only with PORTENV_DEV_PREVIEWS, which `make app` sets
/// for a LANE build and never for a release one; scripts/check-dev-previews.sh
/// keeps it that way.
@MainActor
enum BlocksPreview {
    static let argument = "--blocks-preview"
    private static var window: NSWindow?
    private static var quits: NSObjectProtocol?

    static func show() {
        let day = Calendar.current.startOfDay(for: Date())
        let view = TerminalViews.tab()
        view.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)
        view.frame = NSRect(x: 0, y: 0, width: 980, height: 600)
        view.getTerminal().resize(cols: 110, rows: 30)
        BlockRenders.session(into: view, day: day)
        let reader = BlockReader(view: view)
        let now = day.addingTimeInterval(14 * 3600 + 40 * 60)
        let palette = TerminalPalette.matching(NSApp.effectiveAppearance)
        let root = BlocksView(blocks: reader.blocks(), reader: reader, palette: palette, now: now)
            .frame(minWidth: 720, minHeight: 480)
            .background(SwiftUI.Color(nsColor: .textBackgroundColor))
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 980, height: 640),
                         styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: false)
        w.title = "Blocks preview"
        w.contentView = NSHostingView(rootView: root)
        w.isReleasedWhenClosed = false
        w.center()
        w.makeKeyAndOrderFront(nil)
        NSApp.activate()
        window = w
        // Closing it quits: this window only, never one of macOS's own,
        // like the text input window a click in a block's output closes
        // (R-0272, R-0273).
        quits = QuitWithWindow.watch(w) { exit(0) }
    }
}
#endif
