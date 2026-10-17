// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The main window: a full-width terminal under a title with the box's name
/// and a subtitle with its state. Box actions live in the title menu
/// (Apple's document-menu pattern); in 1.0 only Move To and Revert To work.
struct MainWindow: View {
    @Bindable var controller: BoxController
    @Bindable var tabs: TabsController
    /// The workspace's agent team; nil in renders without one.
    let team: TeamController?
    /// The inspector's open state, remembered for this box's window (§6).
    @AppStorage private var inspectorOpen: Bool
    /// The sidebar as the person chose it with ⌘0, for this box; "" until
    /// they do (TeamSidebarToggle).
    @AppStorage private var sidebarChoice: String
    /// Below 900 pt the sidebar is compact (GUIDELINES 6.2).
    @State private var narrow = false
    /// Renders only (ScreenRenders): a sample terminal in place of
    /// `portenv attach`.
    private let renderTerminal: (() -> NSView)?

    init(controller: BoxController, tabs: TabsController, team: TeamController? = nil, renderTerminal: (() -> NSView)? = nil) {
        self.controller = controller
        self.tabs = tabs
        self.team = team
        self.renderTerminal = renderTerminal
        _inspectorOpen = AppStorage(wrappedValue: false, InspectorToggle.key(controller.box))
        _sidebarChoice = AppStorage(wrappedValue: "", TeamSidebarToggle.key(controller.box))
    }

    /// The selected agent, whose column takes the inspector's place
    /// (R-0240).
    private var agent: TeamAgent? { team?.selectedAgent }

    /// Open while an agent is selected, whatever the person last chose.
    private var columnOpen: Binding<Bool> {
        Binding(get: { inspectorOpen || agent != nil || team?.selectedDecision != nil }, set: { inspectorOpen = $0 })
    }

    var body: some View {
        HStack(spacing: 0) {
            if TeamSidebarToggle.shows(choice: sidebarChoice, team: team), let team {
                TeamSidebar(state: team.state, tabs: tabs.tabs, selection: Bindable(team).selection, compact: narrow)
                Divider()
            }
            content
        }
            // The team while the window is open; it watches again after a
            // restart or a move.
            .task(id: controller.box) { await team?.follow() }
            .onGeometryChange(for: Bool.self) { $0.size.width < 900 } action: { narrow = $0 }
            // The terminal side and the inspector share the window's width;
            // only the window has a minimum width (R-0073, R-0075).
            .boxWindowColumns(inspectorOpen: columnOpen) {
                if let agent, let team {
                    TeamColumn(agent: agent, team: team)
                        .inspectorToggle(isOpen: columnOpen)
                } else if let team, let decision = team.selectedDecision {
                    DecisionColumn(decision: decision, team: team)
                        .inspectorToggle(isOpen: columnOpen)
                } else {
                    inspectorView
                }
            }
            .navigationTitle(controller.box)
            // Keep Waiting's way out (R-0178), next to "Waiting for …".
            .toolbar {
                if let place = controller.waitingFor {
                    ToolbarItem(placement: .primaryAction) {
                        Button(StopWaiting.title) { controller.stopWaiting() }
                            .keyboardShortcut(StopWaiting.shortcut)
                            .help("Stop trying to open it while \(place) holds it")
                    }
                }
            }
            // The window closing ends the wait too; the view merely leaving
            // the window (another state) doesn't (R-0179).
            .background(EndsWithWindow { controller.stopWaiting() })
            // 2.3: open somewhere else, and Move To's confirmation.
            .sheet(isPresented: Binding(get: { controller.leaseSheet != nil }, set: { if !$0 { controller.cancelLeaseSheet() } })) {
                if let sheet = controller.leaseSheet {
                    LeaseSheetView(sheet: sheet, choose: { a in Task { await controller.choose(a) } }, cancel: { controller.cancelLeaseSheet() })
                }
            }
            .sheet(isPresented: Binding(get: { controller.moveConfirmation != nil }, set: { if !$0 { controller.cancelMove() } })) {
                if let m = controller.moveConfirmation {
                    MoveConfirmationView(confirmation: m, move: { Task { await controller.confirmMove() } }, cancel: { controller.cancelMove() })
                }
            }
            // The title is the box's menu (Apple's document-menu pattern):
            // the system title is replaced by BoxTitle, the box's name and
            // sync symbol over its state line, on the toolbar itself with no
            // capsule behind it (the main-window board).
            .toolbar(removing: .title)
            .toolbar {
                ToolbarItem(placement: .navigation) {
                    BoxTitle(controller: controller)
                }
                .sharedBackgroundVisibility(.hidden)
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
            // Told once (R-0201): portenvd restarted the workspace to apply
            // a security update.
            .alert("Workspace Restarted", isPresented: Binding(get: { controller.notice != nil }, set: { if !$0 { controller.dismissNotice() } })) {
                Button("OK") { controller.dismissNotice() }
            } message: {
                Text(controller.notice ?? "")
            }
    }

    private var inspectorView: some View {
                InspectorView(
                    content: InspectorContent(box: controller, tabs: tabs),
                    hasTeam: team?.state.hasTeam ?? false,
                    yourInitial: String(NSFullUserName().prefix(1)).uppercased(),
                    moveMenu: controller.moveMenu,
                    move: { target in Task { await controller.requestMove(to: target) } },
                    run: { action in Task { await controller.run(action) } },
                    retryPackages: { Task { await controller.retryPackages() } },
                    revert: { Task { await controller.revertToLastSavePoint() } },
                    retrySaves: { Task { await controller.refreshSaves() } }
                )
                // Read the saves while open and wherever the box runs,
                // quietly trying again if that fails. A new save reads them
                // again from the controller (savedAtChanged).
                .task(id: controller.location) {
                    await controller.keepSavesLoaded()
                }
                // The toggle at the toolbar's trailing edge, open or closed.
                .inspectorToggle(isOpen: columnOpen)
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
        // Docker Desktop stopped (R-0098): the same words and button as
        // Welcome's card; Restart Box comes back once Docker answers.
        case _ where controller.engineDown:
            VStack(spacing: 10) {
                Text(DockerText.notRunningTitle).font(.title3)
                Text(DockerText.notRunningLine)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                Button(DockerText.openButton) { DockerDesktop.perform(.openDockerDesktop) }
                    .keyboardShortcut(.defaultAction)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(.background)
        case _ where controller.agentUnavailable && controller.isOpen:
            VStack(spacing: 10) {
                Text("The workspace helper is unavailable").font(.title3)
                Text("Nothing can be saved until the workspace restarts. Your unsaved changes stay in the workspace; restarting keeps them.")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                Button("Restart Workspace") { Task { await controller.restartBox() } }
                    .accessibilityLabel("Restart Workspace: the workspace restarts and keeps your unsaved changes")
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
                HStack(spacing: 0) {
                    TabBar(tabs: tabs, selection: team?.selection ?? .allTabs)
                    // An agent's tabs only: All tabs at the trailing end
                    // brings back every tab (R-0241).
                    if let team, team.selection != .allTabs {
                        Button(TeamText.allTabs) { team.selection = .allTabs }
                            .buttonStyle(.link)
                            .padding(.horizontal, 12)
                    }
                }
                    // Follows the tabs while the box is open here or on a
                    // server; watches again after a restart or a move.
                    .task { await tabs.follow() }
                Group {
                    if let renderTerminal {
                        RenderedView(make: renderTerminal)
                    } else {
                        BoxTerminal(box: controller.box, generation: controller.terminalGeneration,
                                    onEnded: { Task { await controller.terminalEnded() } },
                                    onLinkStatus: { controller.linkStatus = $0 })
                    }
                }
                .accessibilityLabel(A11y.terminal(box: controller.box))
            }
        case .closed:
            if controller.busy {
                placeholder(controller.subtitle, "")
            } else {
                ClosedWorkspace(box: controller.box, failure: controller.openFailure) {
                    Task { await controller.open() }
                }
            }
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

/// View › Show Inspector (⌥⌘I), sharing the window's remembered state. Off
/// during first run and the box list, when no box is open.
struct InspectorToggle: Commands {
    let box: String?
    static func key(_ box: String) -> String { "inspectorOpen.\(box)" }

    var body: some Commands {
        CommandGroup(after: .sidebar) {
            if let box {
                InspectorMenuItem(key: Self.key(box)).id(box)
            } else {
                Button("Show Inspector") {}
                    .keyboardShortcut("i", modifiers: [.command, .option])
                    .disabled(true)
            }
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
            // A detail (the last save point's time) is the item's subtitle in
            // the menu bar's menu.
            let button = Button { Task { await controller.run(item.action) } } label: {
                Text(item.title)
                if let detail = item.detail { Text(detail) }
            }
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

/// A closed workspace's window (R-0211): its name, why it isn't open when
/// an open failed (portenvd's plain line), and Open, or Try Again after a
/// failure, as the default button (Return).
struct ClosedWorkspace: View {
    let box: String
    let failure: String?
    let open: () -> Void

    var body: some View {
        VStack(spacing: 10) {
            Text(ClosedText.title(box)).font(.title3)
            if let failure {
                Text(failure)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button(failure == nil ? ClosedText.open : ClosedText.tryAgain, action: open)
                .keyboardShortcut(.defaultAction)
                .accessibilityLabel(ClosedText.buttonLabel(box, failed: failure != nil))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background)
        .accessibilityElement(children: .contain)
    }
}
