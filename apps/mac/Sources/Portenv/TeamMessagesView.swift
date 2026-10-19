// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

// The owner sees every message that involves their agents (ADR 0016 point 5,
// R-0313): the Team section in an agent's column, Team activity, and the
// field to message an agent (R-0314). Board text is shown with
// Text(verbatim:), never as Markdown, links or actions (GUIDELINES 6.2).

/// One board message, one line until clicked: "Lead → Builder", the time
/// and its first line; opened, its text, the first 12 lines or 600
/// characters, then Show all. Portenv's notices are greyed.
struct TeamMessageEntry: View {
    let message: TeamMessage
    let team: [TeamAgent]
    var timeZone: TimeZone = .current
    @State var open = false
    @State var all = false

    var body: some View {
        let notice = TeamMessageText.isNotice(message)
        let preview = TeamMessageText.preview(message.text)
        VStack(alignment: .leading, spacing: 1) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(verbatim: TeamMessageText.route(message, team: team))
                    .font(.system(size: 12, weight: .semibold)).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 4)
                Text(verbatim: TeamMessageText.time(message.createdAt, timeZone: timeZone))
                    .font(.system(size: 11)).foregroundStyle(.secondary).fixedSize()
            }
            if open {
                Text(verbatim: all ? message.text : preview ?? message.text)
                    .font(.system(size: 12.5)).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                if preview != nil, !all {
                    Button(TeamMessageText.showAll) { all = true }
                        .buttonStyle(.link).font(.system(size: 11))
                }
            } else {
                Text(verbatim: TeamMessageText.firstLine(message.text))
                    .font(.system(size: 12)).lineLimit(1).truncationMode(.tail)
            }
        }
        .foregroundStyle(notice ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
        .padding(.vertical, 5).padding(.horizontal, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(open ? AnyShapeStyle(.quaternary.opacity(0.5)) : AnyShapeStyle(.clear), in: RoundedRectangle(cornerRadius: 6))
        .contentShape(Rectangle())
        .onTapGesture { open.toggle() }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(TeamMessageText.spoken(message, team: team, timeZone: timeZone))
        .accessibilityValue(open ? (all ? message.text : preview ?? message.text) : "")
        .accessibilityAddTraits(.isButton)
        .accessibilityAction { open.toggle() }
        .accessibilityAction(named: TeamMessageText.showAll) {
            open = true
            all = true
        }
    }
}

/// The agent's Team section (R-0313): every other message it sent or
/// received, oldest first, collapsed by default.
struct TeamSection: View {
    let messages: [TeamMessage]
    let team: [TeamAgent]
    @Binding var open: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Button {
                open.toggle()
            } label: {
                HStack(spacing: 5) {
                    Image(systemName: open ? "chevron.down" : "chevron.right").font(.system(size: 9, weight: .semibold))
                    Text(TeamMessageText.teamHeading(messages.count)).font(.system(size: 11, weight: .semibold))
                    Spacer(minLength: 0)
                }
                .foregroundStyle(.secondary)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(messages.count == 1 ? "Team, 1 message" : "Team, \(messages.count) messages")
            .accessibilityValue(open ? "opened" : "collapsed")
            if open {
                if messages.isEmpty {
                    Text(TeamMessageText.noTeamMessages).font(.system(size: 12)).foregroundStyle(.secondary)
                }
                ForEach(messages) { m in
                    TeamMessageEntry(message: m, team: team).id(m.id)
                }
            }
        }
    }
}

/// Team activity (R-0313), in the column's place: the whole board, oldest
/// first, live, with a filter by agent.
struct TeamActivityColumn: View {
    @Bindable var team: TeamController
    /// An agent id, or "" for everyone.
    @State var filter = ""

    var body: some View {
        let shown = team.state.activity(filter: filter.isEmpty ? nil : filter)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "bubble.left.and.bubble.right").foregroundStyle(Color.accentColor).accessibilityHidden(true)
                Text(TeamMessageText.activity).font(.system(size: 13, weight: .semibold)).lineLimit(1)
                Spacer(minLength: 4)
                Picker("Show messages of", selection: $filter) {
                    Text(TeamMessageText.everyone).tag("")
                    ForEach(team.state.agents) { a in Text(verbatim: a.name).tag(a.id) }
                }
                .labelsHidden()
                .frame(maxWidth: 120)
                .accessibilityLabel("Show messages of")
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            Divider()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 2) {
                        if shown.isEmpty {
                            Text(TeamMessageText.noActivity).font(.system(size: 12)).foregroundStyle(.secondary)
                        }
                        ForEach(shown) { m in
                            TeamMessageEntry(message: m, team: team.state.agents).id(m.id)
                        }
                    }
                    .padding(14)
                }
                .defaultScrollAnchor(.bottom)
                .onChange(of: shown.last?.id) { _, id in
                    if let id { proxy.scrollTo(id, anchor: .bottom) }
                }
            }
            // An agent filtered on that leaves the team: back to everyone.
            .onChange(of: team.state.agents.map(\.id)) { _, ids in
                if !filter.isEmpty, !ids.contains(filter) { filter = "" }
            }
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }
}

/// Team activity's row at the top of the sidebar.
struct TeamActivityRow: View {
    var compact = false

    var body: some View {
        Group {
            if compact {
                Image(systemName: "bubble.left.and.bubble.right").foregroundStyle(Color.accentColor)
                    .frame(width: 22, height: 22)
                    .help(TeamMessageText.activity)
            } else {
                HStack(spacing: 8) {
                    Image(systemName: "bubble.left.and.bubble.right").foregroundStyle(Color.accentColor)
                        .frame(width: 22, height: 22)
                    Text(TeamMessageText.activity)
                }
                .padding(.vertical, 2)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Team activity, every message on the board")
        .accessibilityAddTraits(.isButton)
    }
}

/// The field to message the selected agent (GUIDELINES 6.2): Return sends,
/// Shift-Return starts a new line, a refusal shows under it in red.
struct MessageComposer: View {
    let agent: TeamAgent
    @Bindable var team: TeamController

    var body: some View {
        let draft = team.draft(for: agent.id)
        let lines = min(5, draft.reduce(1) { $1 == "\n" ? $0 + 1 : $0 })
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .bottom, spacing: 8) {
                MessageField(text: Binding(get: { team.draft(for: agent.id) }, set: { team.setDraft($0, for: agent.id) }),
                             label: TeamMessageText.placeholder(agent.name)) { submit() }
                    .frame(height: CGFloat(lines) * 17 + 11)
                    .overlay(alignment: .topLeading) {
                        if draft.isEmpty {
                            Text(verbatim: TeamMessageText.placeholder(agent.name))
                                .font(.system(size: 13)).foregroundStyle(.tertiary)
                                .padding(.leading, 9).padding(.top, 5).lineLimit(1)
                                .allowsHitTesting(false).accessibilityHidden(true)
                        }
                    }
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color(nsColor: .separatorColor)))
                Button(TeamMessageText.send) { submit() }
                    .disabled(draft.allSatisfy(\.isWhitespace))
                    .accessibilityLabel("Send to \(agent.name)")
            }
            Text(TeamMessageText.hint).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1).minimumScaleFactor(0.8)
            if let refusal = team.refusals[agent.id] {
                Text(verbatim: refusal).font(.system(size: 11)).foregroundStyle(Color(nsColor: .systemRed))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityLabel("Not sent: \(refusal)")
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
    }

    private func submit() {
        let id = agent.id
        Task { await team.submit(to: id) }
    }
}

/// A plain-text, multi-line field: Return sends, Shift-Return inserts a
/// line break. Rich text, pasted images and link detection are off, so what
/// is sent is exactly the characters typed.
struct MessageField: NSViewRepresentable {
    @Binding var text: String
    let label: String
    let submit: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        scroll.drawsBackground = false
        scroll.borderType = .noBorder
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        if let tv = scroll.documentView as? NSTextView {
            tv.delegate = context.coordinator
            tv.isRichText = false
            tv.importsGraphics = false
            tv.allowsUndo = true
            tv.isAutomaticLinkDetectionEnabled = false
            tv.isAutomaticDataDetectionEnabled = false
            tv.isAutomaticQuoteSubstitutionEnabled = false
            tv.isAutomaticDashSubstitutionEnabled = false
            tv.isAutomaticTextReplacementEnabled = false
            tv.drawsBackground = false
            tv.font = .systemFont(ofSize: 13)
            tv.textContainerInset = NSSize(width: 4, height: 5)
            tv.string = text
            tv.setAccessibilityLabel(label)
        }
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        context.coordinator.parent = self
        guard let tv = scroll.documentView as? NSTextView else { return }
        if tv.string != text { tv.string = text }
        tv.setAccessibilityLabel(label)
    }

    @MainActor
    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: MessageField

        init(_ parent: MessageField) { self.parent = parent }

        func textDidChange(_ note: Notification) {
            guard let tv = note.object as? NSTextView else { return }
            parent.text = tv.string
        }

        func textView(_ tv: NSTextView, doCommandBy selector: Selector) -> Bool {
            guard selector == #selector(NSResponder.insertNewline(_:)) else { return false }
            return returnPressed(shift: NSApp.currentEvent?.modifierFlags.contains(.shift) == true, in: tv)
        }

        /// Return sends; Shift-Return inserts a line break where the
        /// cursor is.
        @discardableResult
        func returnPressed(shift: Bool, in tv: NSTextView) -> Bool {
            if shift {
                tv.insertNewlineIgnoringFieldEditor(nil)
                parent.text = tv.string
            } else {
                parent.text = tv.string
                parent.submit()
            }
            return true
        }
    }
}
