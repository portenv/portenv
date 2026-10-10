// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The inspector (GUIDELINES.md §6): Where it is, Saves, Who's here and
/// Running now, always in that order. It explains and offers the obvious
/// action; it never holds settings. Built from plain values so it renders
/// the same from fixtures.
struct InspectorView: View {
    let content: InspectorContent
    /// The first letter of the person's name, for their avatar.
    let yourInitial: String
    /// Where the box can move: (title, target).
    let moveTargets: [(String, String)]
    let move: (String) -> Void
    let retryPackages: () -> Void
    let revert: () -> Void
    /// Off only for offscreen renders (ImageRenderer draws nothing inside a
    /// ScrollView).
    var scrolls = true

    var body: some View {
        if scrolls {
            ScrollView { sections }.accessibilityLabel(A11y.inspector)
        } else {
            sections.accessibilityLabel(A11y.inspector)
        }
    }

    private var sections: some View {
        VStack(alignment: .leading, spacing: 24) {
            whereItIs
            saves
            whosHere
            runningNow
        }
        .padding(.horizontal, 16)
        .padding(.top, 18)
        .padding(.bottom, 24)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: Sections

    @ViewBuilder private var whereItIs: some View {
        Section(InspectorText.whereItIs) {
            if let target = content.movingTo {
                // During a move: what's happening, and what still works.
                Line(InspectorText.moving(to: target), detail: nil)
                Text(InspectorText.moveNote)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                Line(InspectorText.runningOn(content.place),
                     detail: content.openedAt.map { InspectorText.since($0) })
                if let storage = content.storagePlace {
                    Line(InspectorText.savesGoTo(storage), detail: InspectorText.encryption(content.place))
                }
                if let packages = InspectorText.packagesLine(content.failedPackages) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        // Text in the normal colour (contrast, §12), marked
                        // by the symbol, never by colour alone.
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.system(size: 11))
                            .foregroundStyle(.orange)
                            .accessibilityHidden(true)
                        Text(packages).font(.system(size: 12))
                            .fixedSize(horizontal: false, vertical: true)
                        Button("Retry", action: retryPackages)
                            .buttonStyle(.plain)
                            .foregroundStyle(.tint)
                            .font(.system(size: 12))
                            .accessibilityLabel(A11y.retryPackages)
                    }
                }
                Menu("Move To…") {
                    ForEach(moveTargets, id: \.1) { title, target in
                        Button(title) { move(target) }
                    }
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .disabled(moveTargets.isEmpty)
            }
        }
    }

    @ViewBuilder private var saves: some View {
        Section(InspectorText.savesHeading) {
            if let elsewhere = content.savesElsewhere {
                Note(elsewhere)
            } else if content.saves.isEmpty {
                Note(InspectorText.noSaves)
            } else {
                if let last = content.savedAt {
                    Text("Last saved \(InspectorText.clock(last))")
                        .font(.system(size: 12)).foregroundStyle(.secondary)
                }
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(content.saves) { row in SaveRowView(row: row, revert: revert, revealRevert: !scrolls) }
                }
            }
        }
    }

    @ViewBuilder private var whosHere: some View {
        Section(InspectorText.whosHere) {
            HStack(spacing: 8) {
                Avatar(letter: yourInitial)
                VStack(alignment: .leading, spacing: 1) {
                    Text(InspectorText.you).font(.system(size: 12, weight: .medium))
                    Text(content.place ?? "This Mac").font(.system(size: 11)).foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
            Note(InspectorText.noAgents)
        }
    }

    @ViewBuilder private var runningNow: some View {
        Section(InspectorText.runningNow) {
            if content.tabs.isEmpty {
                Note("No tabs open.")
            } else {
                ForEach(content.tabs) { tab in
                    HStack(spacing: 6) {
                        Text(tab.name).font(.system(size: 12))
                        Spacer(minLength: 8)
                        if let program = tab.program {
                            // Only a real program name is in the program style.
                            Text(program).font(.system(size: 11, design: .monospaced)).foregroundStyle(.secondary)
                        } else {
                            Text(InspectorText.idle).font(.system(size: 11)).foregroundStyle(.secondary)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(A11y.runningTab(tab.name, program: tab.program))
                }
            }
        }
    }
}

/// A section: a small grey heading, then its lines.
private struct Section<Content: View>: View {
    let title: String
    @ViewBuilder let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.system(size: 11, weight: .semibold))
                .foregroundStyle(.secondary)
                .accessibilityAddTraits(.isHeader)
            content
        }
    }
}

/// A line with an optional grey detail under it.
private struct Line: View {
    let text: String
    let detail: String?

    init(_ text: String, detail: String?) {
        self.text = text
        self.detail = detail
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(text).font(.system(size: 12, weight: .medium))
            if let detail { Text(detail).font(.system(size: 11)).foregroundStyle(.secondary) }
        }
        .accessibilityElement(children: .combine)
    }
}

/// An empty state: one short sentence.
private struct Note: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(.system(size: 12)).foregroundStyle(.secondary) }
}

/// One save: time and kind; the newest save point offers Revert on hover.
private struct SaveRowView: View {
    let row: InspectorText.SaveRow
    let revert: () -> Void
    /// Renders show Revert without a hover (§6 shows it on hover).
    var revealRevert = false
    @State private var hovering = false

    var body: some View {
        HStack(spacing: 8) {
            Text(InspectorText.clock(row.time))
                .font(.system(size: 11).monospacedDigit())
                .foregroundStyle(.secondary)
                .frame(width: 44, alignment: .leading)
            VStack(alignment: .leading, spacing: 1) {
                Text(row.kind).font(.system(size: 12))
                if let name = row.name {
                    Text("“\(name)”").font(.system(size: 11)).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 4)
            if row.revertable {
                Button("Revert", action: revert)
                    .buttonStyle(.plain)
                    .foregroundStyle(.tint)
                    .font(.system(size: 12))
                    .opacity(hovering || revealRevert ? 1 : 0)
                    .accessibilityLabel(A11y.revertTo(row.kind))
            }
        }
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .accessibilityElement(children: .combine)
    }
}

/// A round avatar with a letter (decorative: the name is next to it).
private struct Avatar: View {
    let letter: String
    var body: some View {
        Text(letter)
            .font(.system(size: 10, weight: .bold))
            .foregroundStyle(.white)
            .frame(width: 22, height: 22)
            .background(Circle().fill(Color.gray))
            .accessibilityHidden(true)
    }
}
