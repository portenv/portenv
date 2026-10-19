// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

extension FocusedValues {
    /// The focused terminal pane's text size (R-0362), for View › Bigger and
    /// Smaller.
    @Entry var terminalTextSize: TerminalTextSizeState?
}

/// View › Bigger (⌘+) and Smaller (⌘−) for the focused terminal (R-0362),
/// as in Terminal.app: only the terminal's text changes. ⌘= and ⌘⇧= also
/// make it bigger, from the terminal itself (MarkedTerminalView). There is
/// no Default Size: Smaller stops at the default, and ⌘0 is View › Show
/// Sidebar (the owner, R-0362).
struct TextSizeCommands: Commands {
    @FocusedValue(\.terminalTextSize) private var size

    var body: some Commands {
        CommandGroup(after: .sidebar) {
            Divider()
            TextSizeMenuItems(size: size)
        }
    }
}

private struct TextSizeMenuItems: View {
    let size: TerminalTextSizeState?

    var body: some View {
        ForEach(TerminalTextSizeMenu.items(points: size?.points), id: \.title) { item in
            Button(item.title) { size?.apply(item.step) }
                .keyboardShortcut(KeyEquivalent(item.key), modifiers: .command)
                .disabled(!item.isEnabled)
                .accessibilityLabel(item.accessibilityLabel)
        }
    }
}
