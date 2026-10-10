// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The boxes on this Mac (PLAN.md 1.5): shown when the box asked for isn't
/// here, or when there are several and none was open last. Never a window
/// for a box that doesn't exist.
struct BoxListView: View {
    let boxes: [String]
    let notice: String?
    let open: (String) -> Void
    let newBox: () -> Void
    @State private var selection: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Your boxes").font(.system(size: 26, weight: .bold))
            if let notice {
                Text(notice)
                    .font(.title3)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            List(boxes, id: \.self, selection: $selection) { box in
                Label(box, systemImage: "shippingbox")
                    .tag(box)
                    .accessibilityLabel("Box \(box)")
                    .accessibilityHint("Opens the box")
            }
            .contextMenu(forSelectionType: String.self) { _ in
                EmptyView()
            } primaryAction: { picked in
                if let box = picked.first { open(box) }
            }
            .frame(minHeight: 180)
            HStack {
                Button("New Box…", action: newBox)
                Spacer()
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
    }
}
