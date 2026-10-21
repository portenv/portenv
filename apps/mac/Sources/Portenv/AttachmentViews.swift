// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI
import UniformTypeIdentifiers

// Attachments in the app (ADR 0030 point 5, GUIDELINES 6.2): a chip per
// attachment under its message or with a decision's evidence, the
// Attachments panel, and the owner's files going in by drop, paste or
// Attach…. An attachment's name is plain text; its contents are decoded
// only in PortenvPreview.xpc, and shown in AttachmentPreview.

/// The chips of one message or decision, wrapping in the column.
struct AttachmentChips: View {
    let attachments: [TeamAttachment]
    /// Who sent them, as people read it ("Builder", "You").
    let from: String
    let team: TeamController
    var at: Date?

    static let frameName = "attachment-chips"

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(attachments.enumerated()), id: \.element.id) { i, a in
                AttachmentChip(attachment: a, from: from, team: team) {
                    AttachmentPreviewWindow.show(team: team, attachments: attachments, index: i, from: from, at: at)
                }
            }
        }
        .reportsFrame(Self.frameName)
    }
}

/// One chip: a thumbnail (images, at most 64 pt high) or a document symbol,
/// then "redesign.md · Markdown · 14 KB". Greyed, with no preview, when
/// this Mac's store doesn't have it.
struct AttachmentChip: View {
    let attachment: TeamAttachment
    let from: String
    let team: TeamController
    let open: () -> Void

    var body: some View {
        let state = team.availability(of: attachment)
        let shown = state != .elsewhere && state != .removed
        Button(action: open) {
            HStack(spacing: 6) {
                if shown, let png = team.thumbnails[attachment.id], let image = NSImage(data: png) {
                    Image(nsImage: image).resizable().interpolation(.medium).aspectRatio(contentMode: .fit)
                        .frame(maxWidth: 96, maxHeight: 64)
                        .clipShape(RoundedRectangle(cornerRadius: 4))
                } else {
                    Image(systemName: AttachmentText.symbol(attachment)).font(.system(size: 13)).frame(width: 18)
                }
                Text(verbatim: AttachmentText.chip(attachment, availability: state))
                    .font(.system(size: 11.5)).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 6).padding(.vertical, 4)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 6).fill(SwiftUI.Color.primary.opacity(0.06)))
            .contentShape(RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .foregroundStyle(shown ? AnyShapeStyle(.primary) : AnyShapeStyle(.tertiary))
        .disabled(!shown)
        .contextMenu {
            if shown {
                Button(AttachmentText.saveToMac) { AttachmentSaving.save(attachment, team: team) }
            }
        }
        .task(id: attachment.id) { await team.check(attachment) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(AttachmentText.spoken(attachment, from: from, availability: state))
        .accessibilityAddTraits(.isButton)
    }
}

/// Save to Mac…: the save panel, proposing the name in Downloads; the file
/// is quarantined and never opened afterwards.
@MainActor
enum AttachmentSaving {
    static func save(_ a: TeamAttachment, team: TeamController) {
        Task {
            do {
                let data = try await team.data(of: a)
                _ = try await AttachmentSave.save(data, name: a.name) { name in
                    let panel = NSSavePanel()
                    panel.nameFieldStringValue = name
                    panel.directoryURL = AttachmentSave.proposedDirectory
                    panel.canCreateDirectories = true
                    return panel.runModal() == .OK ? panel.url : nil
                }
            } catch {
                let alert = NSAlert()
                alert.messageText = "\(a.name) wasn\u{2019}t saved."
                alert.informativeText = (error as? DaemonError)?.message ?? error.localizedDescription
                alert.runModal()
            }
        }
    }
}

/// The Attachments panel (ADR 0030 point 5, R-0381): every attachment on the
/// board, newest first, agent to agent included, whatever is muted. Click
/// opens the preview; ⌘-click goes to its message in Team activity.
struct AttachmentsColumn: View {
    @Bindable var team: TeamController
    @State var filter = ""

    static func rows(_ team: TeamController, filter: String) -> [AttachmentEntry] {
        team.state.attachmentEntries(filter: filter.isEmpty ? nil : filter)
    }

    var body: some View {
        let rows = Self.rows(team, filter: filter)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "paperclip").foregroundStyle(Color.accentColor).accessibilityHidden(true)
                Text(AttachmentText.panelHeading(rows.count)).font(.system(size: 13, weight: .semibold)).lineLimit(1)
                Spacer(minLength: 4)
                Picker("Show attachments of", selection: $filter) {
                    Text(TeamMessageText.everyone).tag("")
                    ForEach(team.state.agents) { a in Text(verbatim: a.name).tag(a.id) }
                }
                .labelsHidden().frame(maxWidth: 120)
                .accessibilityLabel("Show attachments of")
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            Divider()
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 4) {
                    if rows.isEmpty {
                        Text(AttachmentText.noAttachments).font(.system(size: 12)).foregroundStyle(.secondary)
                    }
                    ForEach(rows) { r in
                        AttachmentPanelRow(entry: r, team: team)
                    }
                }
                .padding(14)
            }
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }
}

struct AttachmentPanelRow: View {
    let entry: AttachmentEntry
    let team: TeamController

    var body: some View {
        let a = entry.attachment
        let state = team.availability(of: a)
        let from = TeamMessageText.name(entry.from, team: team.state.agents)
        HStack(alignment: .top, spacing: 8) {
            if let png = team.thumbnails[a.id], let image = NSImage(data: png), state != .removed, state != .elsewhere {
                Image(nsImage: image).resizable().aspectRatio(contentMode: .fit).frame(width: 36, height: 36)
                    .clipShape(RoundedRectangle(cornerRadius: 4))
            } else {
                Image(systemName: AttachmentText.symbol(a)).font(.system(size: 18)).frame(width: 36, height: 36)
            }
            VStack(alignment: .leading, spacing: 1) {
                Text(verbatim: a.name).font(.system(size: 12, weight: .semibold)).lineLimit(1).truncationMode(.middle)
                Text(verbatim: entry.route(team: team.state.agents)).font(.system(size: 11)).lineLimit(1)
                Text(verbatim: [TeamMessageText.time(entry.at), AttachmentText.chip(a, availability: state).components(separatedBy: " \u{00B7} ").dropFirst()
                    .joined(separator: " \u{00B7} ")].filter { !$0.isEmpty }.joined(separator: " \u{00B7} "))
                    .font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .padding(4)
        .contentShape(Rectangle())
        .foregroundStyle(state == .elsewhere || state == .removed ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.primary))
        .onTapGesture {
            if NSEvent.modifierFlags.contains(.command) {
                team.focusedLine = entry.lineID
                team.selection = .activity
            } else if state != .elsewhere, state != .removed {
                let all = team.state.attachmentEntries().filter { $0.lineID == entry.lineID }.map(\.attachment)
                AttachmentPreviewWindow.show(team: team, attachments: all, index: all.firstIndex(of: a) ?? 0, from: from, at: entry.at)
            }
        }
        .task(id: a.id) { await team.check(a) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(AttachmentText.spoken(a, from: from, availability: state) + " " + entry.route(team: team.state.agents))
        .accessibilityAddTraits(.isButton)
        .accessibilityAction(named: "Show its message") {
            team.focusedLine = entry.lineID
            team.selection = .activity
        }
    }
}

/// The View menu's team items: Show Sidebar (⌘0) and Show Attachments
/// (⌥⌘A), as one entry of the app's commands.
struct TeamViewCommands: Commands {
    let box: String?
    let team: TeamController?

    var body: some Commands {
        TeamSidebarToggle(box: box, team: team)
        AttachmentsCommand(team: team)
    }
}

/// View › Show Attachments (⌥⌘A).
struct AttachmentsCommand: Commands {
    static let title = AttachmentText.showAttachments
    static let shortcut = KeyboardShortcut("a", modifiers: [.command, .option])
    let team: TeamController?

    var body: some Commands {
        CommandGroup(after: .sidebar) {
            Button(Self.title) { team?.selection = .attachments }
                .keyboardShortcut(Self.shortcut)
                .disabled(team?.state.hasTeam != true)
        }
    }
}

/// A place the owner's files can be dropped (an agent's column, its
/// message field, its tab, its terminal, Team activity): the app's checks
/// first, then portenvd. While a drag is over it, it says what will happen.
struct AttachDrop: ViewModifier {
    let team: TeamController
    let agents: [String]
    var pane = ""
    let target: String
    @State private var over = false

    func body(content: Content) -> some View {
        content
            .overlay {
                if over {
                    RoundedRectangle(cornerRadius: 8).stroke(Color.accentColor, lineWidth: 2)
                        .allowsHitTesting(false)
                }
            }
            .dropDestination(for: URL.self) { urls, _ in
                guard !agents.isEmpty else { return false }
                Task { await team.attach(urls, to: agents, pane: pane, target: target) }
                return true
            } isTargeted: { over = $0 }
    }
}

extension View {
    func attachDrop(_ team: TeamController?, to agents: [String], pane: String = "", target: String) -> some View {
        Group {
            if let team { modifier(AttachDrop(team: team, agents: agents, pane: pane, target: target)) } else { self }
        }
    }
}

/// Attach…: the open panel, files only, several at once.
@MainActor
enum AttachPicker {
    static func choose() -> [URL] {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = true
        panel.prompt = "Attach"
        return panel.runModal() == .OK ? panel.urls : []
    }
}

/// The paperclip button next to a message field.
struct AttachButton: View {
    let label: String
    let action: () -> Void

    var body: some View {
        Button(action: action) { Image(systemName: "paperclip") }
            .buttonStyle(.borderless)
            .help(AttachmentText.attach)
            .accessibilityLabel(label)
    }
}

/// A refusal under a drop target or field, in red, as one line.
struct AttachRefusal: View {
    let line: String?

    var body: some View {
        if let line {
            Text(verbatim: line).font(.system(size: 11)).foregroundStyle(SwiftUI.Color(nsColor: .systemRed))
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityLabel("Not attached: \(line)")
        }
    }
}

/// The line under the tab bar after a drop typed nothing: where the copy
/// landed, with Copy Path.
struct DropNoticeBar: View {
    @Bindable var team: TeamController

    var body: some View {
        if let n = team.dropNotice {
            HStack(spacing: 8) {
                Image(systemName: "tray.and.arrow.down").foregroundStyle(.secondary).accessibilityHidden(true)
                Text(verbatim: n.line).font(.system(size: 12)).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                Spacer(minLength: 4)
                Button(AttachmentText.copyPath) {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(n.path, forType: .string)
                }
                Button { team.dropNotice = nil } label: { Image(systemName: "xmark") }
                    .buttonStyle(.borderless).accessibilityLabel("Dismiss")
            }
            .padding(.horizontal, 12).padding(.vertical, 5)
            .background(.bar)
        }
    }
}

/// Send to whole team (Team activity's Attach…): each agent with a
/// checkbox, all checked; each chosen agent gets its own copy.
struct SendToTeamSheet: View {
    let agents: [TeamAgent]
    let files: [URL]
    let send: ([String]) -> Void
    let cancel: () -> Void
    @State private var chosen: Set<String>

    init(agents: [TeamAgent], files: [URL], send: @escaping ([String]) -> Void, cancel: @escaping () -> Void) {
        self.agents = agents
        self.files = files
        self.send = send
        self.cancel = cancel
        _chosen = State(initialValue: Set(agents.map(\.id)))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(AttachmentText.sendToWholeTeam).font(.headline)
            Text(verbatim: files.map(\.lastPathComponent).joined(separator: ", ")).font(.system(size: 12)).foregroundStyle(.secondary)
                .lineLimit(2)
            ForEach(agents) { a in
                Toggle(isOn: Binding(get: { chosen.contains(a.id) }, set: { if $0 { chosen.insert(a.id) } else { chosen.remove(a.id) } })) {
                    Text(verbatim: a.name)
                }
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { cancel() }.keyboardShortcut(.cancelAction)
                Button("Send") { send(agents.map(\.id).filter(chosen.contains)) }
                    .keyboardShortcut(.defaultAction)
                    .disabled(chosen.isEmpty)
            }
        }
        .padding(20).frame(width: 340)
    }
}
