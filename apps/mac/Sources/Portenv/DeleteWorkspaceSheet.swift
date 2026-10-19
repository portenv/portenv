// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// Delete Workspace… (ADR 0029, GUIDELINES §3.3): what goes, "This can’t be
/// undone.", and the name typed exactly. Cancel is the default button.
struct DeleteWorkspaceSheet: View {
    @Bindable var flow: DeleteFlow
    let cancel: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let c = flow.confirmation {
                Text(c.title).font(.headline)
                if c.offersClose || c.blocked != nil {
                    blocked(c)
                } else {
                    confirm(c)
                }
            } else if let failure = flow.failure {
                Text(failure).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                HStack { Spacer(); Button("Cancel", action: cancel).keyboardShortcut(.defaultAction) }
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .padding(20)
        .frame(width: 460)
        // ⎋ cancels too, unless a deletion is under way.
        .onExitCommand { if !flow.working { cancel() } }
        .task { await flow.load() }
    }

    /// Open here, or refused: why, and (open here) Close Workspace.
    @ViewBuilder private func blocked(_ c: DeleteConfirmation) -> some View {
        Text(c.blocked ?? "").fixedSize(horizontal: false, vertical: true)
        HStack {
            Spacer()
            Button("Cancel", action: cancel)
                .keyboardShortcut(.defaultAction)
            if c.offersClose {
                Button(BoxListText.closeWorkspace) { Task { await flow.closeFirst() } }
                    .disabled(flow.working)
            }
        }
    }

    @ViewBuilder private func confirm(_ c: DeleteConfirmation) -> some View {
        Text("This deletes:")
        VStack(alignment: .leading, spacing: 4) {
            ForEach(c.removes, id: \.self) { line in
                Label(line, systemImage: "minus.circle").labelStyle(.titleAndIcon)
            }
        }
        .accessibilityElement(children: .combine)
        if let elsewhere = c.savesElsewhere {
            Text(elsewhere).fixedSize(horizontal: false, vertical: true)
            if let second = c.secondConfirmation {
                Toggle(second, isOn: $flow.savesElsewhereConfirmed)
            }
        }
        if let backups = c.backups {
            Text(backups).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        Text(c.cantBeUndone).bold()
        Text(c.typePrompt)
        TextField("", text: $flow.typed)
            .textFieldStyle(.roundedBorder)
            .accessibilityLabel(c.typePrompt)
            .disabled(flow.working)
        if let failure = flow.failure {
            Text(failure).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
        }
        HStack {
            if flow.working {
                if reduceMotion { Text("Deleting…") } else { ProgressView().controlSize(.small); Text("Deleting…") }
            }
            Spacer()
            Button("Cancel", action: cancel)
                .keyboardShortcut(.defaultAction)
                .disabled(flow.working)
            Button(c.deleteButton, role: .destructive) { Task { await flow.delete() } }
                .disabled(!flow.canDelete)
                .accessibilityHint(c.cantBeUndone)
        }
    }
}
