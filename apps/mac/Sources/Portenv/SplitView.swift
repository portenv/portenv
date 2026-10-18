// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import PortenvTerm
import SwiftUI

/// Split view (PLAN 1.2b, the split-view board): up to four panes, laid out
/// by SplitLayout, each streaming its own tab through MainTabTerminal (ADR
/// 0020), with its own Reconnecting and Disconnected. With more than one
/// pane, each has a slim header (the tab picker and a close button), the
/// focused one a 1 px accent outline, and dividers drag within the panes'
/// minimums. With one pane it is the plain terminal, as before.
struct SplitPanes: View {
    @Bindable var split: SplitController
    let provider: any TabTerminalProvider
    let generation: Int
    let box: String
    /// The team, so a pane names whose tab it shows (R-0314).
    var team: [TeamAgent] = []
    var serviceComing = false

    var body: some View {
        GeometryReader { geo in
            let size = geo.size
            let frames = split.layout.frames(in: size)
            ZStack(alignment: .topLeading) {
                ForEach(split.layout.panes, id: \.self) { pane in
                    if let frame = frames[pane], let tab = split.layout.tab(in: pane) {
                        PaneView(split: split, pane: pane, tab: tab, provider: provider, generation: generation,
                                 box: box, team: team, serviceComing: serviceComing)
                            .frame(width: frame.width, height: frame.height)
                            .offset(x: frame.minX, y: frame.minY)
                    }
                }
                ForEach(split.layout.dividers(in: size), id: \.path) { divider in
                    DividerHandle(divider: divider) { fraction in
                        split.resize(at: divider.path, to: fraction)
                    }
                }
            }
            .frame(width: size.width, height: size.height, alignment: .topLeading)
            .coordinateSpace(name: SplitPanes.space)
            .clipped()
        }
        .onGeometryChange(for: CGSize.self) { $0.size } action: { split.size = $0 }
        .accessibilityElement(children: .contain)
    }

    /// The coordinate space dividers measure drags in.
    static let space = "split"
}

/// One pane: its header when split, then its tab's terminal.
private struct PaneView: View {
    @Bindable var split: SplitController
    let pane: PaneID
    let tab: String
    let provider: any TabTerminalProvider
    let generation: Int
    let box: String
    let team: [TeamAgent]
    let serviceComing: Bool

    private var info: TabInfo? { split.tab(in: pane) }
    private var focused: Bool { split.layout.focused == pane }
    /// The tab as the tab bar names it: an agent's with the agent's name.
    private var name: String { info.map { TerminalLabel.title($0, team: team) } ?? "" }
    /// VoiceOver's name, naming whose tab it is (R-0314): "Pane: Lead ·
    /// lead tab"; with one pane, "Terminal: Lead · lead".
    private var label: String {
        if split.isSplit { return SplitText.paneLabel(tab: name, status: nil) }
        return info.map { TerminalLabel.of($0, team: team) } ?? A11y.terminal(box: box)
    }

    var body: some View {
        VStack(spacing: 0) {
            if split.isSplit {
                PaneHeaderBar(split: split, pane: pane, tab: info)
            }
            MainTabTerminal(shown: TabTerminalShown(provider: provider), tab: tab, generation: generation,
                            label: label, serviceComing: serviceComing,
                            isActive: focused, onFocus: { split.focus(pane) })
        }
        // The focused pane's 1 px accent outline (only when split: one pane
        // is always the focused one).
        .overlay {
            if focused && split.isSplit {
                Rectangle().strokeBorder(Color.accentColor, lineWidth: 1)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(label)
        .accessibilityAddTraits(focused && split.isSplit ? .isSelected : [])
    }
}

/// The pane header (the board, slim): the tab picker (the tab's name and a
/// chevron, listing the tabs no other pane shows), the program running in
/// it, and the close button. Closing a pane never stops its tab.
private struct PaneHeaderBar: View {
    @Bindable var split: SplitController
    let pane: PaneID
    let tab: TabInfo?

    var body: some View {
        HStack(spacing: 10) {
            Menu {
                ForEach(split.pickerTabs(for: pane)) { t in
                    Toggle(isOn: Binding(get: { t.id == tab?.id }, set: { if $0 { split.show(t.id, in: pane) } })) {
                        Text(verbatim: t.name)
                    }
                }
            } label: {
                HStack(spacing: 4) {
                    Text(verbatim: tab?.name ?? "").font(.system(size: 12, weight: .semibold))
                    Image(systemName: "chevron.down").font(.system(size: 8, weight: .semibold))
                }
            }
            .menuStyle(.button)
            .buttonStyle(.plain)
            .menuIndicator(.hidden)
            .fixedSize()
            .accessibilityLabel(SplitText.pickerLabel(tab: tab?.name ?? ""))
            // What's running: the one status the tab list carries today.
            Text(verbatim: tab?.program ?? "")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 8)
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
        .padding(.horizontal, 12)
        .frame(height: 28)
        .background(Color.primary.opacity(0.04))
        .overlay(alignment: .bottom) { Divider() }
        .contentShape(Rectangle())
        .onTapGesture { split.focus(pane) }
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
        .gesture(DragGesture(coordinateSpace: .named(SplitPanes.space)).onChanged { g in
            let fraction = vertical ? (g.location.x - span.minX) / max(span.width, 1)
                : (g.location.y - span.minY) / max(span.height, 1)
            resize(Double(fraction))
        })
        .offset(x: vertical ? divider.line.minX - 3 : divider.line.minX,
                y: vertical ? divider.line.minY : divider.line.minY - 3)
        .accessibilityHidden(true)
    }
}

/// The tab bar's mark for a tab in a pane: a small window with the pane's
/// place filled in.
struct PaneGlyph: View {
    let frame: CGRect

    var body: some View {
        let w: CGFloat = 13, h: CGFloat = 10
        ZStack(alignment: .topLeading) {
            RoundedRectangle(cornerRadius: 2).strokeBorder(.secondary, lineWidth: 1)
            Rectangle().fill(.secondary)
                .frame(width: max(2, frame.width * (w - 4)), height: max(2, frame.height * (h - 4)))
                .offset(x: 2 + frame.minX * (w - 4), y: 2 + frame.minY * (h - 4))
        }
        .frame(width: w, height: h)
        .accessibilityHidden(true)
    }
}

/// Split view's commands (PLAN 1.2b), in the Window menu: Split Right ⌘D,
/// Split Down ⇧⌘D, Close Pane, and ⌘⌥ and the arrows between panes. ⌘W
/// (Close Tab) closes the focused pane first (TabCommands).
struct SplitCommands: Commands {
    let split: SplitController?

    var body: some Commands {
        CommandGroup(before: .windowArrangement) {
            // A view, so the items follow the panes as they change.
            SplitMenuItems(split: split)
            Divider()
        }
    }
}

private struct SplitMenuItems: View {
    let split: SplitController?

    var body: some View {
        let isSplit = split?.isSplit ?? false
        Button(SplitText.splitRight) { Task { await split?.split(.right) } }
            .keyboardShortcut("d", modifiers: .command)
            .disabled(!(split?.canSplit(.right) ?? false))
        Button(SplitText.splitDown) { Task { await split?.split(.down) } }
            .keyboardShortcut("d", modifiers: [.command, .shift])
            .disabled(!(split?.canSplit(.down) ?? false))
        Button(SplitText.closePane) { split?.closePane() }
            .disabled(!isSplit)
        Button(SplitText.focusLeft) { split?.moveFocus(.left) }
            .keyboardShortcut(.leftArrow, modifiers: [.command, .option])
            .disabled(!isSplit)
        Button(SplitText.focusRight) { split?.moveFocus(.right) }
            .keyboardShortcut(.rightArrow, modifiers: [.command, .option])
            .disabled(!isSplit)
        Button(SplitText.focusUp) { split?.moveFocus(.up) }
            .keyboardShortcut(.upArrow, modifiers: [.command, .option])
            .disabled(!isSplit)
        Button(SplitText.focusDown) { split?.moveFocus(.down) }
            .keyboardShortcut(.downArrow, modifiers: [.command, .option])
            .disabled(!isSplit)
    }
}

/// The title bar's Split Right and Split Down buttons (the board).
struct SplitToolbarButtons: View {
    @Bindable var split: SplitController

    var body: some View {
        Button { Task { await split.split(.right) } } label: {
            Label(SplitText.splitRight, systemImage: "rectangle.split.2x1")
        }
        .help("\(SplitText.splitRight) (\u{2318}D)")
        .disabled(!split.canSplit(.right))
        Button { Task { await split.split(.down) } } label: {
            Label(SplitText.splitDown, systemImage: "rectangle.split.1x2")
        }
        .help("\(SplitText.splitDown) (\u{21E7}\u{2318}D)")
        .disabled(!split.canSplit(.down))
    }
}
