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
        Button {
            BoxMenuPopup.show(controller)
        } label: {
            HStack(spacing: 5) {
                VStack(alignment: .leading, spacing: 0) {
                    Text(controller.box).font(.headline)
                    Text(controller.subtitle).font(.caption).foregroundStyle(.secondary)
                }
                Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
            }
            .padding(.horizontal, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .fixedSize()
        .accessibilityLabel(controller.box)
        .accessibilityValue(controller.subtitle)
        .help("Box actions")
    }
}

/// The box's menu as a native NSMenu, under the title.
@MainActor
enum BoxMenuPopup {
    static func show(_ controller: BoxController) {
        let menu = NSMenu()
        let move = NSMenuItem(title: "Move To", action: nil, keyEquivalent: "")
        let moveMenu = NSMenu()
        let targets = [("This Mac", "this-mac")] + controller.servers.map { ($0, $0) }
        for (i, (title, id)) in targets.enumerated() {
            if i == 1 { moveMenu.addItem(.separator()) }
            let item = ActionItem(title) { Task { await controller.move(to: id) } }
            // The current location is checked; the others stay available.
            item.state = controller.isCurrent(id) ? .on : .off
            item.isEnabled = !controller.busy
            moveMenu.addItem(item)
        }
        move.submenu = moveMenu
        menu.addItem(move)
        let revert = NSMenuItem(title: "Revert To", action: nil, keyEquivalent: "")
        let revertMenu = NSMenu()
        let last = ActionItem("Last Save Point") { Task { await controller.revertToLastSavePoint() } }
        last.isEnabled = controller.isOpen && !controller.busy
        revertMenu.addItem(last)
        revert.submenu = revertMenu
        menu.addItem(revert)
        menu.addItem(.separator())
        let restart = ActionItem("Restart Box") { Task { await controller.restartBox() } }
        restart.isEnabled = controller.isOpen && !controller.busy
        menu.addItem(restart)
        menu.autoenablesItems = false

        // Under the title: the key window's top-left area, in screen points.
        if let window = NSApp.keyWindow ?? NSApp.windows.first(where: { $0.isVisible }) {
            let point = NSPoint(x: window.frame.minX + 120, y: window.frame.maxY - 54)
            menu.popUp(positioning: nil, at: point, in: nil)
        } else {
            menu.popUp(positioning: nil, at: NSEvent.mouseLocation, in: nil)
        }
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
