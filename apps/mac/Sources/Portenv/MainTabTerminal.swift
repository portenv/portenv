// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import PortenvTerm
import SwiftUI

/// The main window's terminal (ADR 0020): the current tab's own stream from
/// the box, in a tab view (TabTerminalShown). Choosing another tab shows
/// that tab's stream in the same view; a lost stream says "Reconnecting…"
/// while it comes back, and "Disconnected" with Reconnect if it doesn't.
struct MainTabTerminal: View {
    @State var shown: TabTerminalShown
    /// The tab shown: the current one in the tab bar.
    let tab: String
    /// A new generation (the box opened or restarted) opens the stream again.
    let generation: Int
    let label: String
    /// portenvd is away or the workspace is being reopened (R-0242).
    var serviceComing = false
    /// Split view: whether this is the focused pane, whose terminal takes
    /// the keys; and what to do when a click in the terminal focuses it.
    var isActive = true
    var onFocus: () -> Void = {}
    @State private var focused = true

    var body: some View {
        ZStack {
            if let source = shown.source {
                TabTerminalPane(source: source, label: label, isFocused: $focused,
                                onReset: { shown.reset($0) }, onEnded: { shown.ended($0) })
                    .padding(TerminalLayout.margin)
            }
            // The capsule while it comes back; the card (never a modal on
            // top, GUIDELINES.md) once it has given up or the trouble has
            // lasted past the deadline (R-0294).
            switch shown.overlay(serviceComing: serviceComing) {
            case .none:
                EmptyView()
            case .reconnecting:
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text(TabTerminalShown.reconnectingLine)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(.regularMaterial, in: Capsule())
                .frame(maxHeight: .infinity, alignment: .top)
                .padding(.top, 12)
                .accessibilityElement(children: .combine)
            case let .disconnected(why):
                VStack(spacing: 8) {
                    Text("Disconnected").font(.headline)
                    if let why { Text(why).foregroundStyle(.secondary).multilineTextAlignment(.center) }
                    Button("Reconnect") { shown.reconnect() }
                        .keyboardShortcut(.defaultAction)
                }
                .padding(16)
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
                .frame(maxWidth: 420)
                .accessibilityElement(children: .contain)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(nsColor: .textBackgroundColor))
        .onAppear {
            focused = isActive
            shown.show(tab: tab, generation: generation)
        }
        .onChange(of: isActive) { focused = isActive }
        .onChange(of: focused) { if focused { onFocus() } }
        .onChange(of: "\(generation)/\(tab)") { shown.show(tab: tab, generation: generation) }
        .onDisappear { shown.stop() }
    }
}
