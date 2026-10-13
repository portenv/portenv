// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The window's title: the box's name over its state line, and the box's
/// menu on click (Apple's document-menu pattern). A toolbar menu would show
/// only one line of its label, so the title is its own view and the menu a
/// native NSMenu.
struct BoxTitle: View {
    @Bindable var controller: BoxController

    var body: some View {
        // Drawn at the time it's drawn, and again when "Saved just now"
        // becomes "Saved at <time>" (§3.1): savedTimeNow moves then, with no
        // new state to prompt it.
        title(controller.subtitle(at: controller.savedTimeNow))
    }

    private func title(_ subtitle: String) -> some View {
        Button {
            BoxMenuPopup.show(controller)
        } label: {
            // The board's order: the name, its sync symbol and the chevron on
            // the first line, the state line under them (§3.1).
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 5) {
                    Text(controller.box).font(.headline)
                    SyncSymbol(controller: controller).font(.callout)
                    Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
                }
                Text(subtitle).font(.caption).foregroundStyle(.secondary)
            }
            .padding(.horizontal, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .fixedSize()
        .accessibilityLabel(A11y.titleLabel(box: controller.box))
        .accessibilityValue(subtitle)
        .help("Box actions")
    }
}

/// The box's menu as a native NSMenu, under the title (§3.2: the same items,
/// in the same order, as the menu bar's Box menu).
@MainActor
enum BoxMenuPopup {
    static func show(_ controller: BoxController) {
        let optionHeld = NSEvent.modifierFlags.contains(.option)
        let menu = nsMenu(controller.menuItems(optionHeld: optionHeld), controller)
        // Under the title: the key window's top-left area, in screen points.
        if let window = NSApp.keyWindow ?? NSApp.windows.first(where: { $0.isVisible }) {
            let point = NSPoint(x: window.frame.minX + 120, y: window.frame.maxY - 54)
            menu.popUp(positioning: nil, at: point, in: nil)
        } else {
            menu.popUp(positioning: nil, at: NSEvent.mouseLocation, in: nil)
        }
    }

    static func nsMenu(_ items: [BoxMenuItem], _ controller: BoxController) -> NSMenu {
        let menu = NSMenu()
        menu.autoenablesItems = false
        for item in items {
            if item.isSeparator { menu.addItem(.separator()); continue }
            let ns = ActionItem(item.title) { Task { await controller.run(item.action) } }
            ns.isEnabled = item.enabled
            ns.state = item.checked ? .on : .off
            switch item.shortcut {
            case "⌘S": ns.keyEquivalent = "s"; ns.keyEquivalentModifierMask = [.command]
            case "⌥⌘R": ns.keyEquivalent = "r"; ns.keyEquivalentModifierMask = [.command, .option]
            default: break
            }
            if !item.submenu.isEmpty { ns.submenu = nsMenu(item.submenu, controller) }
            menu.addItem(ns)
        }
        return menu
    }
}

/// A menu item that runs a closure.
final class ActionItem: NSMenuItem {
    private let run: () -> Void

    init(_ title: String, run: @escaping () -> Void) {
        self.run = run
        super.init(title: title, action: #selector(fire), keyEquivalent: "")
        target = self
    }

    @available(*, unavailable)
    required init(coder: NSCoder) { fatalError("not used") }

    @objc private func fire() { run() }
}
