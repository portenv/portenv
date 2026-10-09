// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The main window: a full-width terminal under a title with the box's name
/// and a subtitle with its state. Box actions live in the title menu
/// (Apple's document-menu pattern); in 1.0 only Move To and Revert To work.
struct MainWindow: View {
    @Bindable var controller: BoxController

    var body: some View {
        content
            .frame(minWidth: 640, minHeight: 400)
            .navigationTitle(controller.box)
            // The title is the box's menu (Apple's document-menu pattern):
            // the system title is replaced by BoxTitle, the box's name over
            // its state line, next to the sync symbol.
            .toolbar(removing: .title)
            .toolbar {
                ToolbarItem(placement: .navigation) {
                    SyncSymbol(controller: controller)
                }
                ToolbarItem(placement: .navigation) {
                    BoxTitle(controller: controller)
                }
            }
            .disabled(controller.busy)
            // Closing the window doesn't close the box here: the app quits
            // after its last window, and quitting closes it, once (two
            // closes once crashed portenvd).
            .alert("Portenv", isPresented: Binding(get: { controller.error != nil }, set: { if !$0 { controller.dismissError() } })) {
                Button("OK") { controller.dismissError() }
            } message: {
                Text(controller.error ?? "")
            }
    }

    @ViewBuilder private var content: some View {
        switch controller.location {
        case _ where controller.agentUnavailable && controller.isOpen:
            VStack(spacing: 10) {
                Text("The box agent is unavailable").font(.title3)
                Text("Nothing can be saved until the box restarts. Your unsaved changes stay in the box; restarting keeps them.")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                Button("Restart Box") { Task { await controller.restartBox() } }
                    .accessibilityLabel("Restart Box: the box restarts and keeps your unsaved changes")
                    .keyboardShortcut(.defaultAction)
                    .disabled(controller.busy)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(.background)
        case .thisMac, .server:
            // The same terminal wherever the box runs: portenvd reaches a
            // server box's agent through an SSH forward (ADR 0010).
            BoxTerminal(box: controller.box, generation: controller.terminalGeneration) {
                Task { await controller.terminalEnded() }
            }
            .accessibilityLabel(A11y.terminal(box: controller.box))
        case .closed:
            placeholder(controller.busy ? controller.subtitle : "\(controller.box) is closed", "")
        }
    }

    private func placeholder(_ title: String, _ detail: String) -> some View {
        VStack(spacing: 6) {
            Text(title).font(.title3)
            if !detail.isEmpty { Text(detail).foregroundStyle(.secondary) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background)
    }
}

/// The menu bar's Box menu: the same items, in the same order, as the title
/// menu (§3.2), from BoxMenuModel.
struct BoxMenu: View {
    @Bindable var controller: BoxController

    var body: some View {
        ForEach(Array(controller.menuItems(optionHeld: false).enumerated()), id: \.offset) { _, item in
            entry(item)
        }
    }

    @ViewBuilder private func entry(_ item: BoxMenuItem) -> some View {
        if item.isSeparator {
            Divider()
        } else if !item.submenu.isEmpty {
            Menu(item.title) {
                ForEach(Array(item.submenu.enumerated()), id: \.offset) { _, sub in
                    if sub.isSeparator { Divider() } else { leaf(sub) }
                }
            }
            .disabled(!item.enabled)
        } else {
            leaf(item)
        }
    }

    @ViewBuilder private func leaf(_ item: BoxMenuItem) -> some View {
        if case .move = item.action {
            // The current location is checked (macOS convention); every
            // other location stays available.
            Toggle(item.title, isOn: Binding(
                get: { item.checked },
                set: { on in if on { Task { await controller.run(item.action) } } }
            ))
            .disabled(!item.enabled)
        } else {
            let button = Button(item.title) { Task { await controller.run(item.action) } }
                .disabled(!item.enabled)
            switch item.shortcut {
            case "⌘S": button.keyboardShortcut("s", modifiers: .command)
            case "⌥⌘R": button.keyboardShortcut("r", modifiers: [.command, .option])
            default: button
            }
        }
    }
}

/// The sync symbol next to the title (§3.1): none for a new box, spinning
/// while saving or moving (never with Reduce Motion).
struct SyncSymbol: View {
    @Bindable var controller: BoxController
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        // One snapshot gives the symbol, the spin and the text. A symbol
        // that doesn't spin is a plain image with no effect attached, and a
        // new symbol is a new view (keyed by name), so a rotation can never
        // carry over to the checkmark after a save (it once kept turning
        // for seconds next to "Saved just now").
        let look = controller.title
        if let name = look.symbol {
            Group {
                if look.spins && !reduceMotion {
                    Image(systemName: name)
                        .symbolEffect(.rotate, options: .repeat(.continuous))
                } else {
                    Image(systemName: name)
                }
            }
            .id(name + (look.spins ? "/spin" : ""))
            .foregroundStyle(.secondary)
            .help(look.line)
            .accessibilityLabel(A11y.symbolLabel(controller.state))
        }
    }
}
