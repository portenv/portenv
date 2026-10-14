// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The move-and-lease board's sheets on the workspace window (2.3). Escape
/// cancels, Return is the default; VoiceOver reads the title, the body,
/// then the warning box, in that order.

/// "<workspace> is open on <place>" and "<place> isn't answering".
struct LeaseSheetView: View {
    let sheet: LeaseSheet
    let choose: (LeaseSheet.Action) -> Void
    let cancel: () -> Void
    @State private var selected: LeaseSheet.Action?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(sheet.title).font(.headline).accessibilityAddTraits(.isHeader)
            Text(sheet.body).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let detail = sheet.detail {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text(detail).foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
            }
            if let w = sheet.warning { WarningBox(title: w.title, lines: [w.text], monospaced: false) }
            if let note = sheet.footnote {
                Text(note).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if sheet.kind == .openThere {
                ForEach(sheet.choices, id: \.title) { c in
                    ChoiceRow(choice: c, selected: (selected ?? sheet.defaultAction) == c.action) { selected = c.action }
                }
            } else if let move = sheet.choices.first(where: { $0.action == .moveHereTakingOver }), let line = move.detail {
                // R-0168: what moving from a Mac Portenv can't ask means.
                Text(line).font(.callout).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                // Not answering: Cancel on the left, the honest way out with
                // Look hidden (R-0178).
                if sheet.kind == .notAnswering {
                    Button("Cancel", action: cancel).keyboardShortcut(.cancelAction)
                }
                Spacer()
                if sheet.kind != .notAnswering {
                    Button("Cancel", action: cancel).keyboardShortcut(.cancelAction)
                }
                if sheet.kind == .openThere {
                    let action = selected ?? sheet.defaultAction ?? .connect
                    Button(sheet.choices.first { $0.action == action }?.button ?? "Connect") { choose(action) }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut(.defaultAction)
                } else {
                    ForEach(sheet.choices, id: \.title) { c in
                        let isDefault = c.action == sheet.defaultAction
                        if isDefault {
                            Button(c.button) { choose(c.action) }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                        } else {
                            Button(c.button) { choose(c.action) }
                        }
                    }
                }
            }
        }
        .padding(20)
        .frame(width: 440)
        .onExitCommand(perform: cancel) // Escape cancels, on every sheet
    }
}

/// One option on the open-there sheet: a radio card.
private struct ChoiceRow: View {
    let choice: LeaseSheet.Choice
    let selected: Bool
    let pick: () -> Void

    var body: some View {
        Button(action: pick) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: selected ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(selected ? Color.accentColor : .secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text(choice.title).font(.headline)
                    if let d = choice.detail { Text(d).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
                }
                Spacer(minLength: 0)
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
            .background(RoundedRectangle(cornerRadius: 8).fill(selected ? Color.accentColor.opacity(0.12) : .clear))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(selected ? Color.accentColor : Color.secondary.opacity(0.3)))
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }
}

/// "Move <workspace> to <place>?": what stops, and what comes along.
struct MoveConfirmationView: View {
    let confirmation: MoveConfirmation
    let move: () -> Void
    let cancel: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(confirmation.title).font(.headline).accessibilityAddTraits(.isHeader)
            Text(confirmation.body).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if confirmation.running.isEmpty {
                Text(confirmation.runningTitle)
            } else {
                // The command lines are shown here, on this Mac, only (R-0140).
                WarningBox(title: confirmation.runningTitle, lines: confirmation.running, monospaced: true)
            }
            Text(MoveConfirmation.footer).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel", action: cancel).keyboardShortcut(.cancelAction)
                Button("Move", action: move).buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 440)
        .onExitCommand(perform: cancel)
    }
}

/// The board's amber box.
private struct WarningBox: View {
    let title: String
    let lines: [String]
    let monospaced: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.callout.weight(.semibold))
            ForEach(lines, id: \.self) { line in
                Text(line)
                    .font(monospaced ? .callout.monospaced() : .callout)
                    .lineLimit(monospaced ? 1 : nil)
                    .truncationMode(.middle)
                    .fixedSize(horizontal: false, vertical: !monospaced)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.orange.opacity(0.15)))
        .accessibilityElement(children: .combine)
    }
}
