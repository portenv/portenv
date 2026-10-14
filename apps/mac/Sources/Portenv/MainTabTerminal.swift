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
    /// The state line's link text (R-0134).
    var onLinkStatus: (String?) -> Void = { _ in }
    @State private var focused = true

    var body: some View {
        ZStack {
            if let source = shown.source {
                TabTerminalPane(source: source, label: label, isFocused: $focused,
                                onReset: { shown.reset($0) }, onEnded: { shown.ended($0) },
                                onLinkStatus: onLinkStatus)
                    .padding(TerminalLayout.margin)
            }
            switch shown.connection {
            case .connecting, .live:
                EmptyView()
            case .reconnecting:
                Text("Reconnecting…")
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(.regularMaterial, in: Capsule())
                    .frame(maxHeight: .infinity, alignment: .top)
                    .padding(.top, 12)
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
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(nsColor: .textBackgroundColor))
        .onAppear { shown.show(tab: tab, generation: generation) }
        .onChange(of: "\(generation)/\(tab)") { shown.show(tab: tab, generation: generation) }
        .onDisappear { shown.stop() }
    }
}
