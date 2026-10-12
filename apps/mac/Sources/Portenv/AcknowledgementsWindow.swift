// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// Help › Acknowledgements… (R-0096): one plain window with the third-party
/// notices as selectable, scrollable text. No web view.
@MainActor
enum AcknowledgementsWindow {
    private static var window: NSWindow?

    static func show() {
        if let window {
            window.makeKeyAndOrderFront(nil)
            return
        }
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 720, height: 520),
                         styleMask: [.titled, .closable, .miniaturizable, .resizable],
                         backing: .buffered, defer: false)
        w.title = Acknowledgements.windowTitle
        w.contentView = content(Acknowledgements.text(in: .main))
        w.contentMinSize = NSSize(width: 420, height: 300)
        w.isReleasedWhenClosed = false
        w.tabbingMode = .disallowed
        w.center()
        window = w
        w.makeKeyAndOrderFront(nil)
    }

    /// The window's scrolling text: read-only, selectable, with the find bar
    /// (Edit › Find), in the system's text colours so it follows light and
    /// dark. The component list at the top gets a hanging indent.
    static func content(_ string: String) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        guard let text = scroll.documentView as? NSTextView else { return scroll }
        text.isEditable = false
        text.isSelectable = true
        text.isRichText = false
        text.usesFindBar = true
        text.isIncrementalSearchingEnabled = true
        text.drawsBackground = true
        text.backgroundColor = .textBackgroundColor
        text.textColor = .textColor
        text.textContainerInset = NSSize(width: 12, height: 12)
        let font = NSFont.monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        text.font = font
        let styled = NSMutableAttributedString(string: string, attributes: [.font: font, .foregroundColor: NSColor.textColor])
        if let list = Acknowledgements.listRange(in: string) {
            let hanging = NSMutableParagraphStyle()
            hanging.headIndent = ("    " as NSString).size(withAttributes: [.font: font]).width
            styled.addAttribute(.paragraphStyle, value: hanging, range: list)
        }
        text.textStorage?.setAttributedString(styled)
        text.setAccessibilityLabel(Acknowledgements.windowTitle)
        return scroll
    }

    /// File › Close (⌘W) closes this window when it's in front, rather than a
    /// tab of the box behind it.
    static func closeIfKey() -> Bool {
        guard let window, window.isKeyWindow else { return false }
        window.performClose(nil)
        return true
    }
}
