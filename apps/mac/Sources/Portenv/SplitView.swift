// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import PortenvTerm
import SwiftUI

/// Split view (PLAN 1.2b, the split-view board): up to four panes, each its
/// own viewer showing one tab, laid out by SplitLayout. Each pane is a slim
/// header over one tab's terminal (TabTerminalPane, ADR 0020); the focused
/// pane has a 1 px accent outline; dividers drag within the panes' minimums.
struct SplitView: View {
    @Bindable var split: SplitController
    let sources: PaneSources
    /// The time the header's "quiet since" is measured from.
    var now: () -> Date = { Date() }

    var body: some View {
        GeometryReader { geo in
            let size = geo.size
            let frames = split.layout.frames(in: size)
            ZStack(alignment: .topLeading) {
                ForEach(split.layout.panes, id: \.self) { pane in
                    if let frame = frames[pane] {
                        PaneView(split: split, pane: pane, source: sources.source(for: pane), now: now())
                            .frame(width: frame.width, height: frame.height)
                            .offset(x: frame.minX, y: frame.minY)
                    }
                }
                ForEach(split.layout.dividers(in: size), id: \.path) { divider in
                    DividerHandle(divider: divider) { fraction in
                        split.resize(at: divider.path, to: fraction, in: size)
                    }
                }
            }
            .frame(width: size.width, height: size.height, alignment: .topLeading)
            .coordinateSpace(name: SplitView.space)
        }
        .accessibilityElement(children: .contain)
    }
}

/// One pane: its header, then its tab's terminal.
private struct PaneView: View {
    @Bindable var split: SplitController
    let pane: PaneID
    let source: (any TabTerminalSource)?
    let now: Date

    private var tab: TabInfo? { split.tab(split.layout.tab(in: pane)) }
    private var focused: Bool { split.layout.focused == pane }
    private var header: PaneHeader { tab.map { PaneHeader($0, now: now) } ?? PaneHeader(detail: "", who: "") }
    private var label: String { header.label(tab: tab?.name ?? "") }

    var body: some View {
        VStack(spacing: 0) {
            PaneHeaderBar(split: split, pane: pane, tab: tab, header: header)
            Group {
                if let source {
                    TabTerminalPane(source: source, label: label,
                                    isFocused: Binding(get: { focused }, set: { if $0 { split.focus(pane) } }))
                } else {
                    Color(nsColor: .textBackgroundColor)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        // The focused pane's 1 px accent outline (only when split: one pane
        // is always the focused one).
        .overlay {
            if focused && split.isSplit {
                Rectangle().strokeBorder(Color.accentColor, lineWidth: 1).allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
        }
        .contentShape(Rectangle())
        .simultaneousGesture(TapGesture().onEnded { split.focus(pane) })
        .accessibilityElement(children: .contain)
        .accessibilityLabel(label)
        .accessibilityAddTraits(focused ? .isSelected : [])
    }
}

/// The pane header (the board): the tab picker (the tab's name and a
/// chevron), what it's doing, who is typing, and the close button.
private struct PaneHeaderBar: View {
    @Bindable var split: SplitController
    let pane: PaneID
    let tab: TabInfo?
    let header: PaneHeader

    var body: some View {
        HStack(spacing: 10) {
            Menu {
                ForEach(split.tabs) { t in
                    Button(t.name) { Task { await split.show(t.id) } }
                }
            } label: {
                HStack(spacing: 4) {
                    Text(tab?.name ?? "").font(.system(size: 12, weight: .semibold))
                    Image(systemName: "chevron.down").font(.system(size: 8, weight: .semibold))
                }
            }
            .menuStyle(.button)
            .buttonStyle(.plain)
            .menuIndicator(.hidden)
            .fixedSize()
            .simultaneousGesture(TapGesture().onEnded { split.focus(pane) })
            .accessibilityLabel(SplitText.pickerLabel(tab: tab?.name ?? ""))
            Text(header.detail)
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 8)
            Text(header.who)
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
                .lineLimit(1)
            if split.isSplit {
                Button {
                    split.closePane(pane)
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 9, weight: .semibold))
                        .frame(width: 16, height: 16)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .help(SplitText.closePane)
                .accessibilityLabel(SplitText.closeLabel(tab: tab?.name ?? ""))
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 28)
        .background(Color.primary.opacity(0.04))
        .overlay(alignment: .bottom) { Divider() }
    }
}

/// A divider: a hairline, with a wider invisible handle to drag.
private struct DividerHandle: View {
    let divider: SplitLayout.Divider
    let resize: (Double) -> Void

    var body: some View {
        let vertical = divider.axis == .right
        let span = divider.span
        ZStack {
            Rectangle().fill(Color(nsColor: .separatorColor))
                .frame(width: vertical ? 1 : divider.line.width, height: vertical ? divider.line.height : 1)
        }
        .frame(width: vertical ? 7 : divider.line.width, height: vertical ? divider.line.height : 7)
        .contentShape(Rectangle())
        .onHover { inside in
            if inside { (vertical ? NSCursor.resizeLeftRight : NSCursor.resizeUpDown).push() } else { NSCursor.pop() }
        }
        .gesture(DragGesture(coordinateSpace: .named(SplitView.space)).onChanged { g in
            let fraction = vertical ? (g.location.x - span.minX) / max(span.width, 1)
                : (g.location.y - span.minY) / max(span.height, 1)
            resize(Double(fraction))
        })
        .offset(x: vertical ? divider.line.minX - 3 : divider.line.minX,
                y: vertical ? divider.line.minY : divider.line.minY - 3)
        .accessibilityHidden(true)
    }
}

extension SplitView {
    /// The coordinate space dividers measure drags in.
    static let space = "split"
}
