// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm

/// The words on the terminal's right-click menu.
public enum TerminalMenu {
    public static let copy = "Copy"
    public static let paste = "Paste"
    public static let selectAll = "Select All"
    public static let find = FindMenu.find
}

/// The terminal's right-click menu (basics audit, R-0384), as in Terminal:
/// Copy (with a selection), Paste, Select All and Find…. Every item targets
/// this view, so it acts on the pane that was clicked. Paste is the only one
/// that sends anything to the tab, through the view's own `paste(_:)`
/// (cleaned, and bracketed when the program asked). Drops and Services stay
/// refused (R-0167). SwiftTerm 1.20.0 sends no right-click to the program,
/// so the menu shows whatever the program asked of the mouse.
extension MarkedTerminalView {
    public override func menu(for _: NSEvent) -> NSMenu? {
        let menu = NSMenu(title: "")
        // Enabled here, from the view's own validation, rather than by the
        // responder chain, which starts at whichever pane has the keys.
        menu.autoenablesItems = false
        func add(_ title: String, _ action: Selector, tag: Int = 0) {
            let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
            item.target = self
            item.tag = tag
            item.isEnabled = validateUserInterfaceItem(item)
            menu.addItem(item)
        }
        add(TerminalMenu.copy, #selector(TerminalView.copy(_:)))
        add(TerminalMenu.paste, #selector(TerminalView.paste(_:)))
        menu.addItem(.separator())
        add(TerminalMenu.selectAll, #selector(NSResponder.selectAll(_:)))
        menu.addItem(.separator())
        // The tag carries the action, as for the Edit menu's Find… (FindMenu).
        add(TerminalMenu.find, #selector(NSResponder.performTextFinderAction(_:)),
            tag: NSTextFinder.Action.showFindInterface.rawValue)
        return menu
    }
}
