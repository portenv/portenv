// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The main window: a full-width terminal under a title with the box's name
/// and a subtitle with its state. Box actions live in the title menu
/// (Apple's document-menu pattern); in 1.0 only Move To and Revert To work.
struct MainWindow: View {
    @Bindable var controller: BoxController
    @Bindable var tabs: TabsController
    /// The inspector's open state, remembered for this box's window (§6).
    @AppStorage private var inspectorOpen: Bool

    init(controller: BoxController, tabs: TabsController) {
        self.controller = controller
        self.tabs = tabs
        _inspectorOpen = AppStorage(wrappedValue: false, InspectorToggle.key(controller.box))
    }

    var body: some View {
        content
            .frame(minWidth: 640, minHeight: 400)
            .inspector(isPresented: $inspectorOpen) {
                InspectorView(
                    content: InspectorContent(box: controller, tabs: tabs),
                    yourInitial: String(NSFullUserName().prefix(1)).uppercased(),
                    moveTargets: moveTargets,
                    move: { target in Task { await controller.move(to: target) } },
                    retryPackages: { Task { await controller.retryPackages() } },
                    revert: { Task { await controller.revertToLastSavePoint() } }
                )
                .inspectorColumnWidth(min: 260, ideal: 300, max: 400)
                // The saves change with each save: read them while open.
                .task(id: controller.state?.savedAt) { await controller.refreshSaves() }
            }
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
                ToolbarItem(placement: .primaryAction) {
                    // ⌥⌘I is on View › Show Inspector (InspectorToggle).
                    Button { inspectorOpen.toggle() } label: { Image(systemName: "sidebar.right") }
                        .help(inspectorOpen ? A11y.hideInspector : A11y.showInspector)
                        .accessibilityLabel(inspectorOpen ? A11y.hideInspector : A11y.showInspector)
                }
            }
            // Busy disables the window, except Cancel while the Keychain asks.
            .disabled(controller.busy && !controller.waitingForKeychain)
            // Closing the window doesn't close the box here: the app quits
            // after its last window, and quitting closes it, once (two
            // closes once crashed portenvd).
            .alert("Portenv", isPresented: Binding(get: { controller.error != nil }, set: { if !$0 { controller.dismissError() } })) {
                Button("OK") { controller.dismissError() }
            } message: {
                Text(controller.error ?? "")
            }
    }

    /// Move To… in the inspector: every place but where the box is.
    private var moveTargets: [(String, String)] {
        (["this-mac"] + controller.servers)
            .filter { !controller.isCurrent($0) }
            .map { (ServerName.display($0), $0) }
    }

    @ViewBuilder private var content: some View {
        switch controller.location {
        // Also while reopening after portenvd restarted, so Cancel is
        // always in reach while macOS asks.
        case _ where controller.waitingForKeychain:
            VStack(spacing: 10) {
                Text(KeychainWait.line).font(.title3)
                Text(KeychainWait.detail).foregroundStyle(.secondary)
                Button("Cancel") { controller.cancelKeychainWait() }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityLabel("Cancel opening \(controller.box)")
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(.background)
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
            // server box's agent through an SSH forward (ADR 0010). The tab
            // bar sits above it (§3); choosing a tab shows that tmux window
            // in this same terminal.
            VStack(spacing: 0) {
                TabBar(tabs: tabs)
                    // Follows the tabs while the box is open here or on a
                    // server; watches again after a restart or a move.
                    .task { await tabs.follow() }
                BoxTerminal(box: controller.box, generation: controller.terminalGeneration) {
                    Task { await controller.terminalEnded() }
                }
                .accessibilityLabel(A11y.terminal(box: controller.box))
            }
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

/// View › Show Inspector (⌥⌘I), sharing the window's remembered state.
struct InspectorToggle: Commands {
    let box: String
    static func key(_ box: String) -> String { "inspectorOpen.\(box)" }

    var body: some Commands {
        CommandGroup(after: .sidebar) {
            InspectorMenuItem(key: Self.key(box))
        }
    }
}

private struct InspectorMenuItem: View {
    @AppStorage private var open: Bool
    init(key: String) { _open = AppStorage(wrappedValue: false, key) }
    var body: some View {
        Button(open ? "Hide Inspector" : "Show Inspector") { open.toggle() }
            .keyboardShortcut("i", modifiers: [.command, .option])
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
