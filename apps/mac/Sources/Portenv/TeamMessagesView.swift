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
    /// For the chips of its attachments (ADR 0030); nil shows none.
    var controller: TeamController?
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
            if let controller, !message.attachments.isEmpty {
                AttachmentChips(attachments: message.attachments, from: TeamMessageText.name(message.from, team: team), team: controller,
                                at: message.createdAt)
                    .padding(.top, 3)
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
    var controller: TeamController?
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
                    TeamMessageEntry(message: m, team: team, controller: controller).id(m.id)
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
    /// Files chosen with Attach…, waiting for Send to whole team.
    @State private var picked: [URL] = []

    var body: some View {
        let shown = team.state.activity(filter: filter.isEmpty ? nil : filter)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "bubble.left.and.bubble.right").foregroundStyle(Color.accentColor).accessibilityHidden(true)
                Text(TeamMessageText.activity).font(.system(size: 13, weight: .semibold)).lineLimit(1)
                Spacer(minLength: 4)
                // Every attachment on the board (R-0381).
                Button(AttachmentText.panelHeading(team.state.attachmentEntries().count)) { team.selection = .attachments }
                    .buttonStyle(.link).font(.system(size: 11)).lineLimit(1).fixedSize()
                    .accessibilityLabel("Show attachments")
                AttachButton(label: "Attach files for the team") { picked = AttachPicker.choose() }
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
                            TeamMessageEntry(message: m, team: team.state.agents, controller: team, open: team.focusedLine == m.id).id(m.id)
                        }
                        AttachRefusal(line: team.attachRefusals["activity"])
                    }
                    .padding(14)
                }
                .defaultScrollAnchor(.bottom)
                .onChange(of: shown.last?.id) { _, id in
                    if let id { proxy.scrollTo(id, anchor: .bottom) }
                }
                // ⌘-click in the Attachments panel: its message.
                .onChange(of: team.focusedLine, initial: true) { _, id in
                    if let id { proxy.scrollTo(id, anchor: .center) }
                }
            }
            // An agent filtered on that leaves the team: back to everyone.
            .onChange(of: team.state.agents.map(\.id)) { _, ids in
                if !filter.isEmpty, !ids.contains(filter) { filter = "" }
            }
        }
        .frame(maxHeight: .infinity, alignment: .top)
        .sheet(isPresented: Binding(get: { !picked.isEmpty }, set: { if !$0 { picked = [] } })) {
            SendToTeamSheet(agents: team.state.agents.filter { !$0.remote }, files: picked, send: { ids in
                let urls = picked
                picked = []
                Task { await team.attach(urls, to: ids, target: "activity") }
            }, cancel: { picked = [] })
        }
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
                             label: TeamMessageText.placeholder(agent.name), pasteImage: { png in
                                 let id = agent.id
                                 Task { await team.attach([OutgoingAttachments.pasted(png: png)], to: [id], target: id) }
                             }) { submit() }
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
                AttachButton(label: "Attach files for \(agent.name)") {
                    let urls = AttachPicker.choose()
                    let id = agent.id
                    if !urls.isEmpty { Task { await team.attach(urls, to: [id], target: id) } }
                }
                Button(TeamMessageText.send) { submit() }
                    .disabled(draft.allSatisfy(\.isWhitespace))
                    .accessibilityLabel("Send to \(agent.name)")
            }
            .attachDrop(team, to: [agent.id], target: agent.id)
            Text(TeamMessageText.hint).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1).minimumScaleFactor(0.8)
            AttachRefusal(line: team.attachRefusals[agent.id])
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
    /// A pasted image, as PNG: it goes as an attachment, never as text.
    var pasteImage: (Data) -> Void = { _ in }
    let submit: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = PasteAwareTextView.scrollableTextView()
        scroll.drawsBackground = false
        scroll.borderType = .noBorder
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        if let tv = scroll.documentView as? NSTextView {
            (tv as? PasteAwareTextView)?.pasteImage = { context.coordinator.parent.pasteImage($0) }
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

/// The message field's text view: a pasted image (and nothing else on the
/// pasteboard but pictures) is attached, never typed; everything else
/// pastes as plain text.
final class PasteAwareTextView: NSTextView {
    var pasteImage: ((Data) -> Void)?

    override class func scrollableTextView() -> NSScrollView {
        let scroll = NSScrollView()
        let tv = PasteAwareTextView()
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.textContainer?.widthTracksTextView = true
        scroll.documentView = tv
        return scroll
    }

    override func paste(_ sender: Any?) {
        let pb = NSPasteboard.general
        if pb.string(forType: .string) == nil, let image = NSImage(pasteboard: pb), let tiff = image.tiffRepresentation,
           let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) {
            pasteImage?(png)
            return
        }
        super.paste(sender)
    }
}
