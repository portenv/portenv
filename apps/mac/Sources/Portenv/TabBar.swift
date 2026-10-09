// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The tab bar (GUIDELINES.md §3, §4.1): 30 pt under the toolbar, one tab
/// per tmux window in the box, `+` for a new tab. Tabs share the width
/// equally (the main-window mockup). tmux's own status bar is always
/// hidden; the app owns names and order.
struct TabBar: View {
    @Bindable var tabs: TabsController
    /// The tab being renamed, and the name being typed.
    @State private var renaming: String?
    @State private var draft = ""

    var body: some View {
        HStack(spacing: 0) {
            ForEach(tabs.tabs) { tab in
                TabItem(tab: tab, canClose: tabs.canClose, renaming: renaming == tab.id, draft: $draft,
                        select: { Task { await tabs.select(tab.id) } },
                        close: { Task { await tabs.close(tab.id) } },
                        startRename: { draft = tab.name; renaming = tab.id },
                        finishRename: { commit in
                            let id = tab.id, name = draft
                            renaming = nil
                            if commit { Task { await tabs.rename(id, to: name) } }
                        })
                Divider()
            }
            Button {
                Task { await tabs.newTab() }
            } label: {
                Image(systemName: "plus")
                    .font(.system(size: 12, weight: .medium))
                    .frame(width: 36, height: 30)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .help(A11y.newTab)
            .accessibilityLabel(A11y.newTab)
        }
        .frame(height: 30)
        .background(Color(nsColor: .underPageBackgroundColor))
        .overlay(alignment: .bottom) { Divider() }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(A11y.tabBar)
        .alert("Portenv", isPresented: Binding(get: { tabs.error != nil }, set: { if !$0 { tabs.dismissError() } })) {
            Button("OK") { tabs.dismissError() }
        } message: {
            Text(tabs.error ?? "")
        }
    }
}

/// One tab: its name, centred; the close button on hover (never on the last
/// tab); double-click or Rename… in its menu to rename it in place.
private struct TabItem: View {
    let tab: TabInfo
    let canClose: Bool
    let renaming: Bool
    @Binding var draft: String
    let select: () -> Void
    let close: () -> Void
    let startRename: () -> Void
    let finishRename: (_ commit: Bool) -> Void
    @State private var hovering = false
    @FocusState private var fieldFocused: Bool

    var body: some View {
        ZStack {
            if renaming {
                TextField(A11y.renameTab(tab.name), text: $draft)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(size: 12))
                    .padding(.horizontal, 8)
                    .focused($fieldFocused)
                    .onAppear { fieldFocused = true }
                    .onSubmit { finishRename(true) }
                    .onExitCommand { finishRename(false) }
                    .onChange(of: fieldFocused) { _, focused in if !focused { finishRename(true) } }
                    .accessibilityLabel(A11y.renameTab(tab.name))
            } else {
                Text(tab.name)
                    .font(.system(size: 12))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .foregroundStyle(tab.active ? .primary : .secondary)
                    .padding(.horizontal, 28)
            }
            if canClose && hovering && !renaming {
                HStack {
                    Button(action: close) {
                        Image(systemName: "xmark")
                            .font(.system(size: 9, weight: .semibold))
                            .frame(width: 16, height: 16)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .help(A11y.closeTab(tab.name))
                    .accessibilityLabel(A11y.closeTab(tab.name))
                    Spacer()
                }
                .padding(.leading, 8)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(tab.active ? Color(nsColor: .windowBackgroundColor) : .clear)
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .onTapGesture(count: 2) { startRename() }
        .onTapGesture { select() }
        .contextMenu {
            Button("Rename…", action: startRename)
            Button("Close Tab", action: close).disabled(!canClose)
        }
        .accessibilityElement(children: renaming ? .contain : .ignore)
        .accessibilityLabel(A11y.tab(tab.name))
        .accessibilityAddTraits(tab.active ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction(.default, select)
        .accessibilityAction(named: "Rename", startRename)
        .accessibilityActions {
            if canClose { Button(A11y.closeTab(tab.name), action: close) }
        }
    }
}
