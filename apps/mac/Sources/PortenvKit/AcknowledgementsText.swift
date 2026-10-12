// SPDX-License-Identifier: Apache-2.0

import AppKit

/// The Acknowledgements window's scrolling text (R-0096, R-0119): read-only,
/// selectable, with the find bar (Edit › Find), in the system's text colours
/// so it follows light and dark. The component list at the top gets a
/// hanging indent.
@MainActor
public enum AcknowledgementsText {
    public static func view(_ string: String) -> NSScrollView {
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
}
