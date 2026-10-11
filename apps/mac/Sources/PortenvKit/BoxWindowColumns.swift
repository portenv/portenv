// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// The box window's two columns: the tab bar and terminal, and the
/// inspector beside them (§3, §6).
///
/// The terminal side has no minimum width of its own: the window's minimum
/// (AppRoot's, 640 pt) covers both columns. A minimum on the terminal side
/// alone made SwiftUI lay it out wider than what the inspector left, centred
/// and cut on both sides, when the window was narrow with the inspector open:
/// the start of each terminal line (the prompt) and the inspector's trailing
/// half went out of sight (R-0073, R-0075, R-0076).
public struct BoxWindowColumns<Inspector: View>: ViewModifier {
    @Binding var inspectorOpen: Bool
    let inspector: Inspector

    /// The inspector column's width, in points.
    public static var inspectorWidth: (min: CGFloat, ideal: CGFloat, max: CGFloat) { (260, 300, 400) }

    public init(inspectorOpen: Binding<Bool>, @ViewBuilder inspector: () -> Inspector) {
        _inspectorOpen = inspectorOpen
        self.inspector = inspector()
    }

    public func body(content: Content) -> some View {
        content
            .frame(maxWidth: .infinity, minHeight: 400, maxHeight: .infinity)
            .inspector(isPresented: $inspectorOpen) {
                let width = Self.inspectorWidth
                inspector.inspectorColumnWidth(min: width.min, ideal: width.ideal, max: width.max)
            }
    }
}

extension View {
    /// The box window's columns: this view (the tab bar and terminal), and
    /// the inspector beside it while open.
    public func boxWindowColumns(inspectorOpen: Binding<Bool>, @ViewBuilder inspector: () -> some View) -> some View {
        modifier(BoxWindowColumns(inspectorOpen: inspectorOpen, inspector: inspector))
    }
}
