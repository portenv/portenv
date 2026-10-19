// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The boxes on this Mac (PLAN.md 1.5): shown when the box asked for isn't
/// here, or when there are several and none was open last. Never a window
/// for a box that doesn't exist.
struct BoxListView: View {
    let boxes: [String]
    let notice: String?
    /// Try Again under the notice, while portenvd isn't answering (R-0121).
    var retry: (() -> Void)?
    /// The workspaces running on this Mac (ADR 0023), shown as "Running",
    /// with Close Workspace.
    var running: Set<String> = []
    var close: (String) -> Void = { _ in }
    /// Reads `running` again when the list shows.
    var refresh: () async -> Void = {}
    let open: (String) -> Void
    let newBox: () -> Void
    @State private var selection: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Your workspaces").font(.system(size: 26, weight: .bold))
                .accessibilityAddTraits(.isHeader)
            if let notice {
                Text(notice)
                    .font(.title3)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let retry {
                Button(InspectorText.tryAgain, action: retry)
            }
            List(boxes, id: \.self, selection: $selection) { box in
                HStack {
                    Label(box, systemImage: "shippingbox")
                    Spacer()
                    if running.contains(box) {
                        Text(BoxListText.running).foregroundStyle(.secondary)
                    }
                }
                .tag(box)
                .accessibilityElement(children: .combine)
                .accessibilityLabel(BoxListText.label(box, running: running.contains(box)))
                .accessibilityHint("Opens the workspace")
            }
            .contextMenu(forSelectionType: String.self) { picked in
                if let box = picked.first, running.contains(box) {
                    Button(BoxListText.closeWorkspace) { close(box) }
                }
            } primaryAction: { picked in
                if let box = picked.first { open(box) }
            }
            .frame(minHeight: 180)
            HStack {
                Button("New Workspace…", action: newBox)
                Spacer()
                if let selection, running.contains(selection) {
                    Button(BoxListText.closeWorkspace) { close(selection) }
                }
                Button("Open") { if let selection { open(selection) } }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(selection == nil)
            }
        }
        .padding(.horizontal, 64)
        .padding(.vertical, 48)
        .frame(minWidth: 720, minHeight: 520)
        .background(.background)
        .onAppear { selection = selection ?? boxes.first }
        .task { await refresh() }
    }
}
