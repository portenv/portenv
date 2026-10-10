// SPDX-License-Identifier: Apache-2.0

import AppKit
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
    /// The ⌃Tab / ⌃⇧Tab monitor, seen before the terminal gets the key.
    @State private var keyMonitor: Any?

    var body: some View {
        HStack(spacing: 0) {
            ForEach(tabs.tabs) { tab in
                TabItem(tab: tab, canClose: tabs.canClose, renaming: renaming == tab.id, draft: $draft,
                        select: { Task { await tabs.select(tab.id) } },
                        close: { Task { _ = await tabs.requestClose(id: tab.id) } },
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
        // The bar is the window colour, tinted; the selected tab is the
        // plain window colour, as in the mockup (semantic colours, §11).
        .background(Color.primary.opacity(0.06))
        .background(Color(nsColor: .windowBackgroundColor))
        .overlay(alignment: .bottom) { Divider() }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(A11y.tabBar)
        .alert("Portenv", isPresented: Binding(get: { tabs.error != nil }, set: { if !$0 { tabs.dismissError() } })) {
            Button("OK") { tabs.dismissError() }
        } message: {
            Text(tabs.error ?? "")
        }
        .onAppear {
            keyMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
                let m = event.modifierFlags
                guard let step = TabKeys.step(keyCode: event.keyCode, control: m.contains(.control), shift: m.contains(.shift),
                                              option: m.contains(.option), command: m.contains(.command)) else { return event }
                Task { step > 0 ? await tabs.selectNext() : await tabs.selectPrevious() }
                return nil
            }
        }
        .onDisappear {
            if let keyMonitor { NSEvent.removeMonitor(keyMonitor) }
            keyMonitor = nil
        }
        // A tab that still runs a program asks before it closes (§4.1).
        .alert(tabs.pendingClose.map(TabText.closeQuestion) ?? "",
               isPresented: Binding(get: { tabs.pendingClose != nil }, set: { if !$0 { tabs.cancelClose() } }),
               presenting: tabs.pendingClose) { asked in
            Button(TabText.closeButton, role: .destructive) { Task { await tabs.confirmClose(asked) } }
            Button("Cancel", role: .cancel) { tabs.cancelClose() }
        }
    }
}

/// The tab shortcuts (§4.1), in the menu bar: File › New Tab ⌘T and Close
/// Tab ⌘W (the last tab closes the window, which saves and releases the
/// box), Window › Show Previous Tab ⌘⇧[, Show Next Tab ⌘⇧] and Show Tab
/// ▸ 1–9 (⌘1–⌘9). ⌃Tab and ⌃⇧Tab are handled by the tab bar's key monitor.
struct TabCommands: Commands {
    /// The open box's tabs; none during first run or the box list, when the
    /// tab commands are off and ⌘W closes the window.
    let tabs: TabsController?

    var body: some Commands {
        CommandGroup(after: .newItem) {
            Button("New Tab") { Task { await tabs?.newTab() } }
                .keyboardShortcut("t", modifiers: .command)
                .disabled(tabs == nil)
            Button(TabText.closeButton) {
                guard let tabs else { NSApp.keyWindow?.performClose(nil); return }
                Task {
                    if await tabs.requestClose() == .closeWindow { NSApp.keyWindow?.performClose(nil) }
                }
            }
            .keyboardShortcut("w", modifiers: .command)
        }
        CommandGroup(before: .windowArrangement) {
            Button("Show Previous Tab") { Task { await tabs?.selectPrevious() } }
                .keyboardShortcut("[", modifiers: [.command, .shift])
                .disabled(tabs == nil)
            Button("Show Next Tab") { Task { await tabs?.selectNext() } }
                .keyboardShortcut("]", modifiers: [.command, .shift])
                .disabled(tabs == nil)
            Menu("Show Tab") {
                ForEach(1...9, id: \.self) { n in
                    Button("\(n)") { Task { await tabs?.select(number: n) } }
                        .keyboardShortcut(KeyEquivalent(Character("\(n)")), modifiers: .command)
                }
            }
            .disabled(tabs == nil)
            Divider()
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
                    .font(.system(size: 12, weight: tab.active ? .semibold : .regular))
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
        // The current tab is marked by more than a shade (§4.1, §12): its
        // name in semibold and an accent line along its bottom edge.
        .overlay(alignment: .bottom) {
            if tab.active { Rectangle().fill(Color.accentColor).frame(height: 2).accessibilityHidden(true) }
        }
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
