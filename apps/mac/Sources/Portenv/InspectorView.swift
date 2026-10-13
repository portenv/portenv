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
    /// Move To ▸'s places, as in the title menu (BoxMenuModel.moveSubmenu).
    let moveMenu: [BoxMenuItem]
    let move: (String) -> Void
    let retryPackages: () -> Void
    let revert: () -> Void
    /// Try Again when the saves couldn't be loaded.
    var retrySaves: () -> Void = {}
    /// Off only for offscreen renders (ImageRenderer draws nothing inside a
    /// ScrollView).
    var scrolls = true

    var body: some View {
        // One container VoiceOver names "Inspector" (R-0027).
        Group {
            if scrolls { ScrollView { sections } } else { sections }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(A11y.inspector)
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
                // Each with its symbol, as on the main-window board: where it
                // runs, and the lock for where its saves go.
                Line(InspectorText.runningOn(content.place),
                     detail: content.openedAt.map { InspectorText.since($0) },
                     symbol: content.place == nil ? "laptopcomputer" : "server.rack")
                if let storage = content.storagePlace {
                    Line(InspectorText.savesGoTo(storage), detail: InspectorText.encryption(content.place, savesTo: storage),
                         symbol: "lock")
                }
                if let packages = InspectorText.packagesLine(content.failedPackages) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        // Text in the normal colour (contrast, §12), marked
                        // by the symbol, never by colour alone.
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.system(size: 11))
                            .foregroundStyle(.orange)
                            .accessibilityHidden(true)
                        Text(packages).font(.system(size: 13))
                            .fixedSize(horizontal: false, vertical: true)
                        Button("Retry", action: retryPackages)
                            .buttonStyle(.plain)
                            .foregroundStyle(.tint)
                            .font(.system(size: 13))
                            .accessibilityLabel(A11y.retryPackages)
                    }
                }
                // A push button that opens the title menu's Move To ▸: the
                // current place checked, the others, then Add a Server…
                // With no server it still opens (R-0142, move-and-lease).
                Menu("Move To…") {
                    ForEach(Array(moveMenu.enumerated()), id: \.offset) { _, item in
                        if item.isSeparator {
                            Divider()
                        } else if case let .move(target) = item.action {
                            Toggle(item.title, isOn: Binding(get: { item.checked },
                                                             set: { on in if on { move(target) } }))
                                .disabled(!item.enabled)
                        } else {
                            Button(item.title) {}.disabled(!item.enabled)
                        }
                    }
                }
                .menuStyle(.button)
                .menuIndicator(.hidden)
                .fixedSize()
                .help("Move this workspace to another place")
            }
        }
    }

    @ViewBuilder private var saves: some View {
        Section(InspectorText.savesHeading) {
            switch InspectorText.savesShown(loaded: content.savesLoaded, failed: content.savesFailed,
                                            rows: content.saves, elsewhere: content.savesElsewhere) {
            case let .elsewhere(line):
                Note(line)
            case .loading:
                Note(InspectorText.loadingSaves)
            case .failed:
                // Portenv keeps trying quietly; Try Again asks now.
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Note(InspectorText.savesFailed)
                    Button(InspectorText.tryAgain, action: retrySaves)
                        .buttonStyle(.plain)
                        .foregroundStyle(.tint)
                        .font(.system(size: 12))
                }
            case .empty:
                Note(InspectorText.noSaves)
            case .listed:
                if let last = content.savedAt {
                    // The title's rule, from the same saved-at time (§3.1),
                    // worded at content.now, which moves when "just now"
                    // becomes the time.
                    Text(SavedTime.inspector(last, now: content.now))
                        .font(.system(size: 13, weight: .semibold))
                }
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(content.saves.enumerated()), id: \.element.id) { i, row in
                        SaveRowView(row: row, revert: revert, revealRevert: !scrolls)
                        if i < content.saves.count - 1 { Divider() }
                    }
                }
                // The full timeline isn't built yet (§6: the link below the
                // saves); dimmed, as the title menu's Browse All Saves… is.
                Button(InspectorText.browseAllSaves) {}
                    .buttonStyle(.link)
                    .font(.system(size: 12))
                    .disabled(true)
            }
        }
    }

    @ViewBuilder private var whosHere: some View {
        Section(InspectorText.whosHere) {
            HStack(spacing: 8) {
                Avatar(letter: yourInitial)
                VStack(alignment: .leading, spacing: 1) {
                    Text(InspectorText.you).font(.system(size: 13))
                    Text(content.place ?? "This Mac").font(.system(size: 12)).foregroundStyle(.secondary)
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
                // One label/value row, as on the board (R-0142): the tab
                // bar beside it already shows each tab.
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text(InspectorText.tabsLabel).foregroundStyle(.secondary)
                    Spacer(minLength: 8)
                    Text(InspectorText.tabNames(content.tabs)).multilineTextAlignment(.trailing)
                }
                .font(.system(size: 13))
                .accessibilityElement(children: .combine)
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

/// A line with an optional grey detail under it, and an optional symbol
/// before both (decorative: the words say it).
private struct Line: View {
    let text: String
    let detail: String?
    var symbol: String?

    init(_ text: String, detail: String?, symbol: String? = nil) {
        self.text = text
        self.detail = detail
        self.symbol = symbol
    }

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            if let symbol {
                Image(systemName: symbol)
                    .font(.system(size: 16, weight: .light))
                    .foregroundStyle(.secondary)
                    .frame(width: 20, height: 20)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(text).font(.system(size: 13, weight: .semibold))
                if let detail { Text(detail).font(.system(size: 12)).foregroundStyle(.secondary) }
            }
            .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
    }
}

/// An empty state: one short sentence.
private struct Note: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(.system(size: 12)).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
}

/// One save: time and kind; the newest save point offers Revert on hover.
private struct SaveRowView: View {
    let row: InspectorText.SaveRow
    let revert: () -> Void
    /// Renders show Revert without a hover (§6 shows it on hover).
    var revealRevert = false
    @State private var hovering = false
    /// Tab reaches Revert: it shows while it has keyboard focus too.
    @FocusState private var revertFocused: Bool

    var body: some View {
        let shown = row.revertable && (hovering || revertFocused || revealRevert)
        HStack(spacing: 10) {
            Text(InspectorText.clock(row.time))
                .font(.system(size: 13).monospacedDigit())
                .foregroundStyle(.secondary)
                .frame(width: 40, alignment: .leading)
            VStack(alignment: .leading, spacing: 1) {
                Text(row.kind).font(.system(size: 13))
                if let name = row.name {
                    Text("“\(name)”").font(.system(size: 12)).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 4)
            if row.revertable {
                // A small push button on the hovered row (the board's
                // Revert); hidden otherwise, as §6 asks.
                Button("Revert", action: revert)
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .opacity(shown ? 1 : 0)
                    .focused($revertFocused)
                    .accessibilityHidden(true) // the row's action below says it once
            }
        }
        .frame(minHeight: 32)
        .padding(.horizontal, 6)
        .background(RoundedRectangle(cornerRadius: 6).fill(shown ? Color.primary.opacity(0.07) : .clear))
        .padding(.horizontal, -6)
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .accessibilityElement(children: .combine)
        // VoiceOver and the keyboard reach Revert without hovering (R-0027).
        .accessibilityActions {
            ForEach(A11y.saveRowActions(row), id: \.self) { name in
                Button(name, action: revert)
            }
        }
    }
}

/// A round avatar with a letter (decorative: the name is next to it).
private struct Avatar: View {
    let letter: String
    var body: some View {
        Text(letter)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(.white)
            .frame(width: 26, height: 26)
            .background(Circle().fill(Color.gray))
            .accessibilityHidden(true)
    }
}
