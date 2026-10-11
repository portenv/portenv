// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// The inspector toggle, the toolbar's last item (GUIDELINES §4), placed in
/// the inspector's own toolbar section as Xcode does: right-aligned above
/// the inspector column when it's open, at the window's trailing edge when
/// it's closed, in the same place both ways (R-0069). On macOS
/// `.primaryAction` is the leading edge, next to the title, where it once sat.
public struct InspectorToggleToolbar: ViewModifier {
    @Binding var isOpen: Bool

    public init(isOpen: Binding<Bool>) { _isOpen = isOpen }

    public func body(content: Content) -> some View {
        content.toolbar {
            ToolbarSpacer(.flexible)
            ToolbarItem {
                // ⌥⌘I is on View › Show Inspector (InspectorToggle). One
                // label, from the Label (R-0027).
                Button { isOpen.toggle() } label: {
                    Label(isOpen ? A11y.hideInspector : A11y.showInspector, systemImage: "sidebar.right")
                        .labelStyle(.iconOnly)
                }
                .help(isOpen ? A11y.hideInspector : A11y.showInspector)
            }
        }
    }
}

extension View {
    /// The inspector toggle; applied to the inspector's content (R-0069).
    public func inspectorToggle(isOpen: Binding<Bool>) -> some View {
        modifier(InspectorToggleToolbar(isOpen: isOpen))
    }
}
