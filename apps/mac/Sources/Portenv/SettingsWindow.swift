// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Portenv › Settings… (⌘,), the settings board (R-0137): a standard macOS
/// preferences window, one toolbar item per pane, the window titled with the
/// pane shown. Built on NSTabViewController's toolbar style, so it's the
/// system's own preferences toolbar.
@MainActor
enum SettingsWindow {
    private static var controller: NSWindowController?

    /// Shows the window (one only), on the pane given or the last one.
    static func show(_ pane: SettingsPane? = nil) {
        let wc = controller ?? make()
        controller = wc
        if let pane, let tabs = wc.contentViewController as? SettingsTabs {
            tabs.selectedTabViewItemIndex = SettingsPane.allCases.firstIndex(of: pane) ?? 0
        }
        wc.showWindow(nil)
        wc.window?.makeKeyAndOrderFront(nil)
        NSApp.activate()
    }

    /// The window, made fresh. Renders pass their own window and samples.
    static func make(window: NSWindow? = nil, sample: SettingsSample? = nil) -> NSWindowController {
        let tabs = SettingsTabs(sample: sample)
        let w = window ?? NSWindow(contentRect: NSRect(x: 0, y: 0, width: 640, height: 520),
                                   styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
        w.contentViewController = tabs
        w.toolbarStyle = .preference
        w.isReleasedWhenClosed = false
        w.setContentSize(NSSize(width: 640, height: 520))
        w.center()
        tabs.titleWindow()
        return NSWindowController(window: w)
    }
}

/// The five panes as toolbar items.
final class SettingsTabs: NSTabViewController {
    init(sample: SettingsSample? = nil) {
        super.init(nibName: nil, bundle: nil)
        tabStyle = .toolbar
        for pane in SettingsPane.allCases {
            let host = NSHostingController(rootView: SettingsPaneView(pane: pane, sample: sample))
            host.sizingOptions = []
            let item = NSTabViewItem(viewController: host)
            item.label = pane.title
            item.image = NSImage(systemSymbolName: pane.symbol, accessibilityDescription: pane.title)
            item.identifier = pane.rawValue
            addTabViewItem(item)
        }
    }

    @available(*, unavailable)
    required init?(coder _: NSCoder) { fatalError("not used") }

    override func tabView(_ tabView: NSTabView, didSelect item: NSTabViewItem?) {
        super.tabView(tabView, didSelect: item)
        titleWindow()
    }

    /// The window is titled with the pane shown, as macOS's own are.
    func titleWindow() {
        let i = selectedTabViewItemIndex
        guard i >= 0, i < tabViewItems.count else { return }
        view.window?.title = tabViewItems[i].label
    }
}

/// One pane. Each row is one VoiceOver element: its label, its state, then
/// the line under it (R-0137).
struct SettingsPaneView: View {
    let pane: SettingsPane
    /// Renders only: what the panes show, instead of asking portenvd and
    /// the Keychain.
    var sample: SettingsSample?

    var body: some View {
        Group {
            switch pane {
            case .general: GeneralPane()
            case .servers: ServersPane(sample: sample?.servers)
            case .agents: AgentsPane()
            case .recoveryKey: RecoveryKeyPane(sample: sample?.recoveryKey)
            case .updates: UpdatesPane()
            }
        }
        .formStyle(.grouped)
        .frame(width: 640, height: 470)
    }
}

/// Sample contents for renders.
struct SettingsSample {
    var servers: [String]
    var recoveryKey: RecoveryKeyState
}

/// A row: a label with the line under it, and its state or control at the
/// trailing edge.
private struct SettingRow<Trailing: View>: View {
    let label: String
    var line: String?
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                if let line {
                    Text(line).font(.callout).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 8)
            trailing
        }
        .accessibilityElement(children: .combine)
    }
}

extension SettingRow where Trailing == EmptyView {
    init(label: String, line: String? = nil) {
        self.init(label: label, line: line) { EmptyView() }
    }
}

private struct GeneralPane: View {
    var body: some View {
        Form {
            Section {
                SettingRow(label: SettingsText.appearance, line: SettingsText.appearanceLine) {
                    Text(SettingsText.appearanceState).foregroundStyle(.secondary)
                }
                SettingRow(label: SettingsText.terminalFont) {
                    Text(SettingsText.terminalFontState).foregroundStyle(.secondary)
                }
            }
        }
    }
}

/// The servers portenvd knows, by name. Each server's state, the … menu
/// and Add a Server… come with server setup (2.9, R-0136).
private struct ServersPane: View {
    var sample: [String]?
    @State private var servers: [String]?

    var body: some View {
        Form {
            Section(SettingsText.yourServers) {
                if let servers, !servers.isEmpty {
                    ForEach(servers, id: \.self) { s in SettingRow(label: ServerName.display(s)) }
                } else {
                    SettingRow(label: servers == nil ? "" : SettingsText.noServers)
                }
            }
        }
        .task {
            if let sample { servers = sample; return }
            servers = (try? await SharedDaemon.client.servers()) ?? []
        }
    }
}

/// Until 2.4, only who has access: no agent can connect yet. The SSH door
/// comes back with 2.4, as on the board: PREVIEW and off (R-0149).
private struct AgentsPane: View {
    var body: some View {
        Form {
            Section(SettingsText.agentsWithAccess) {
                SettingRow(label: SettingsText.noAgents)
            }
        }
    }
}

/// What's there today: the recovery key's state and where saves go. Check
/// your key and Make a new key come with ADR 0019's AddRecoveryKey, after
/// first run's wiring; until then they're not shown (R-0149).
private struct RecoveryKeyPane: View {
    var sample: RecoveryKeyState?
    @State private var state: RecoveryKeyState?

    var body: some View {
        Form {
            Section(SettingsText.recoveryHeading) {
                // The board's row: the state is the label ("Saved on 12 Oct"),
                // with what the key does under it, and a check once saved.
                let row = state?.row()
                SettingRow(label: row?.state ?? "", line: row?.line) {
                    if case .made = state {
                        Image(systemName: "checkmark").foregroundStyle(.green).accessibilityHidden(true)
                    }
                }
            }
            Section {
                SettingRow(label: SettingsText.thisMacOnly, line: SettingsText.thisMacOnlyLine)
            } header: {
                Text(SettingsText.whereSavesGo)
            } footer: {
                Text(SettingsText.encryptedNote).font(.callout).foregroundStyle(.secondary)
            }
        }
        .task {
            if let sample { state = sample; return }
            // The pool's attributes only, off the main thread (never its
            // data, so never a Keychain prompt for the recovery passwords).
            let store = SystemSecretStore.recoveryPool
            state = await Task.detached { RecoveryKeyState { try store.createdAt(account: RecoveryPool.account) } }.value
        }
    }
}

/// Until Sparkle (1.8): the version, and how new versions come. The
/// automatic check and Check Now come back with Sparkle, working (R-0149).
/// Release notes are hidden too: there's no release with notes yet.
private struct UpdatesPane: View {
    private let facts = Feedback.Facts.current()
    /// "Count me in usage numbers", the same choice as first run's (R-0126).
    @AppStorage(UsageCounting.choiceKey) private var countUsage = false

    var body: some View {
        Form {
            Section(SettingsText.updatesHeading) {
                SettingRow(label: SettingsText.version(facts), line: SettingsText.downloadForNow)
            }
            Section(SettingsText.privacyHeading) {
                if UsageCounting.shown {
                    CountingChoice(countUsage: Binding(get: { countUsage },
                                                       set: { UsageCounting.setChoice($0) }))
                }
                SettingRow(label: SettingsText.whatPortenvSends,
                           line: SettingsText.whatPortenvSendsLine(counting: countUsage && UsageCounting.shown)) {
                    Link(SettingsText.readPrivacyPage, destination: SettingsText.privacyURL)
                }
                SettingRow(label: SettingsText.feedback, line: Feedback.address) {
                    Button(Feedback.menuItem) { SendFeedback.open() }
                        .buttonStyle(.link)
                }
            }
        }
    }
}

/// Settings for `--render-screens`: each pane, light and dark, in the real
/// window, with the board's sample servers and a saved recovery key.
@MainActor
enum SettingsRender {
    static func render(to dir: URL) async throws {
        let saved = Calendar.current.date(from: DateComponents(year: 2026, month: 10, day: 12, hour: 21)) ?? Date()
        let sample = SettingsSample(servers: ["portenv@test-server"], recoveryKey: .made(saved))
        let before = NSApp.appearance
        defer { NSApp.appearance = before }
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            NSApp.appearance = NSAppearance(named: appearance)
            for pane in SettingsPane.allCases {
                let window = ActiveLookingWindow(contentRect: NSRect(x: 0, y: 0, width: 640, height: 520),
                                                 styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
                window.appearance = NSAppearance(named: appearance)
                let wc = SettingsWindow.make(window: window, sample: sample)
                if let tabs = wc.contentViewController as? SettingsTabs {
                    tabs.selectedTabViewItemIndex = SettingsPane.allCases.firstIndex(of: pane) ?? 0
                    tabs.titleWindow()
                }
                try await WindowCapture.capture(window, to: dir.appendingPathComponent("settings-\(pane.rawValue)-\(suffix).png"))
            }
        }
    }
}
