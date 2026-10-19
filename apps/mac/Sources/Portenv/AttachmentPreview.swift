// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

// The preview (ADR 0030 point 5): a Quick Look-style panel over the window,
// one attachment at a time, ← and → between a message's attachments, Esc,
// Space or ⌘W to close. Images and PDF pages come from PortenvPreview.xpc as
// bounded bitmaps; Markdown is drawn by Portenv's own views; text shows its
// control characters as escapes. Nothing in it opens another app, except a
// link the person clicks, by the link rules.

@MainActor
enum AttachmentPreviewWindow {
    private static var panel: NSPanel?

    static func show(team: TeamController, attachments: [TeamAttachment], index: Int, from: String, at: Date?) {
        guard attachments.indices.contains(index) else { return }
        let p = panel ?? makePanel()
        panel = p
        p.contentView = NSHostingView(rootView: AttachmentPreview(team: team, attachments: attachments, index: index, from: from, at: at) {
            p.orderOut(nil)
        })
        if !p.isVisible { p.center() }
        p.makeKeyAndOrderFront(nil)
    }

    private static func makePanel() -> NSPanel {
        // Not a sheet: it doesn't block the window.
        let p = PreviewPanel(contentRect: NSRect(x: 0, y: 0, width: 760, height: 620),
                             styleMask: [.titled, .closable, .resizable, .utilityWindow, .fullSizeContentView], backing: .buffered, defer: false)
        p.isFloatingPanel = true
        p.hidesOnDeactivate = true
        p.isReleasedWhenClosed = false
        p.titleVisibility = .hidden
        p.minSize = NSSize(width: 360, height: 300)
        return p
    }
}

/// Esc and Space close it; ⌘W too (the panel's own Close).
final class PreviewPanel: NSPanel {
    override func cancelOperation(_: Any?) { orderOut(nil) }
    override func keyDown(with event: NSEvent) {
        if event.charactersIgnoringModifiers == " " { orderOut(nil) } else { super.keyDown(with: event) }
    }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if event.modifierFlags.contains(.command), event.charactersIgnoringModifiers == "w" {
            orderOut(nil)
            return true
        }
        return super.performKeyEquivalent(with: event)
    }
}

struct AttachmentPreview: View {
    let team: TeamController
    let attachments: [TeamAttachment]
    @State var index: Int
    let from: String
    let at: Date?
    let close: () -> Void
    @State private var removing = false

    init(team: TeamController, attachments: [TeamAttachment], index: Int, from: String, at: Date?, close: @escaping () -> Void) {
        self.team = team
        self.attachments = attachments
        _index = State(initialValue: index)
        self.from = from
        self.at = at
        self.close = close
    }

    var body: some View {
        let a = attachments[index]
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Button { index -= 1 } label: { Image(systemName: "chevron.left") }
                    .disabled(index == 0).keyboardShortcut(.leftArrow, modifiers: [])
                    .accessibilityLabel("Previous attachment")
                Button { index += 1 } label: { Image(systemName: "chevron.right") }
                    .disabled(index >= attachments.count - 1).keyboardShortcut(.rightArrow, modifiers: [])
                    .accessibilityLabel("Next attachment")
                Text(verbatim: AttachmentText.header(a, from: from, at: at)).font(.system(size: 12, weight: .semibold))
                    .lineLimit(1).truncationMode(.middle)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                Button(AttachmentText.saveToMac) { AttachmentSaving.save(a, team: team) }
                    .accessibilityLabel(AttachmentText.saveLabel(a))
                Menu {
                    Button(AttachmentText.removeFromMac, role: .destructive) { removing = true }
                } label: { Image(systemName: "ellipsis.circle") }
                    .menuStyle(.borderlessButton).fixedSize()
                    .accessibilityLabel("More for \(a.name)")
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
            Divider()
            AttachmentContent(team: team, attachment: a).id(a.id)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 360, minHeight: 300)
        .confirmationDialog(AttachmentText.removeConfirm, isPresented: $removing) {
            Button(AttachmentText.removeFromMac, role: .destructive) {
                Task {
                    await team.remove(a)
                    close()
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(AttachmentText.removeDetail)
        }
    }
}

/// The attachment itself, by kind.
struct AttachmentContent: View {
    let team: TeamController
    let attachment: TeamAttachment
    @State private var loaded: Loaded = .loading

    enum Loaded {
        case loading
        case failed(String)
        case image(NSImage)
        case pdf(Data, Int)
        case markdown([AttachmentMarkdown.Block])
        case text(String)
        case saveOnly
    }

    var body: some View {
        Group {
            switch loaded {
            case .loading: ProgressView().controlSize(.small)
            case .failed(let line):
                VStack(spacing: 10) {
                    Text(verbatim: line).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    Button(AttachmentText.saveToMac) { AttachmentSaving.save(attachment, team: team) }
                }
                .padding(24)
            case .saveOnly:
                VStack(spacing: 10) {
                    Image(systemName: "doc").font(.system(size: 40)).foregroundStyle(.secondary)
                    Text(verbatim: AttachmentText.chip(attachment))
                    Text("Portenv doesn\u{2019}t preview Word documents. Save it to your Mac to open it.").foregroundStyle(.secondary)
                    Button(AttachmentText.saveToMac) { AttachmentSaving.save(attachment, team: team) }
                }
                .padding(24)
            case .image(let image): ZoomingImage(image: image, name: attachment.name)
            case .pdf(let data, let pages): PDFPages(team: team, data: data, pages: pages)
            case .markdown(let blocks): ScrollView { MarkdownBlocks(blocks: blocks).padding(20) }
            case .text(let text):
                ScrollView {
                    Text(verbatim: AttachmentText.visible(text)).font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(20)
                }
            }
        }
        .task(id: attachment.id) { await load() }
    }

    private func load() async {
        let a = attachment
        guard a.kind.previewable else {
            loaded = .saveOnly
            return
        }
        do {
            let data = try await team.data(of: a)
            switch a.kind {
            case .png, .jpeg, .gif, .webp:
                let screen = NSScreen.main.map { max($0.frame.width, $0.frame.height) * $0.backingScaleFactor } ?? 2048
                let png = try await team.previewer.thumbnail(data, maxPixel: Int(screen))
                guard let image = NSImage(data: png) else { throw PreviewFailure(AttachmentText.couldntShow) }
                loaded = .image(image)
            case .pdf:
                loaded = .pdf(data, try await team.previewer.pdfPageCount(data))
            case .markdown:
                loaded = .markdown(AttachmentMarkdown.parse(data))
            case .text:
                loaded = .text(String(decoding: data, as: UTF8.self))
            case .docx, .other:
                loaded = .saveOnly
            }
        } catch let f as PreviewFailure {
            loaded = .failed(f.line)
        } catch let e as DaemonError {
            loaded = .failed(e.message)
        } catch {
            loaded = .failed(AttachmentText.couldntShow)
        }
    }
}

/// An image fitted, with zoom up to its decoded size: ⌘+, ⌘−, ⌘0 for fit.
struct ZoomingImage: View {
    let image: NSImage
    let name: String
    @State private var zoom: CGFloat?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        GeometryReader { g in
            let fit = min(g.size.width / max(image.size.width, 1), g.size.height / max(image.size.height, 1), 1)
            let scale = zoom ?? fit
            ScrollView([.horizontal, .vertical]) {
                Image(nsImage: image).resizable().interpolation(.high)
                    .frame(width: image.size.width * scale, height: image.size.height * scale)
                    .frame(minWidth: g.size.width, minHeight: g.size.height)
                    .accessibilityLabel(name)
            }
            .gesture(MagnifyGesture().onChanged { v in zoom = min(max((zoom ?? fit) * v.magnification, 0.05), 1) })
            .background {
                Group {
                    Button("Zoom In") { zoom = min((zoom ?? fit) * 1.25, 1) }.keyboardShortcut("+", modifiers: .command)
                    Button("Zoom Out") { zoom = max((zoom ?? fit) / 1.25, 0.05) }.keyboardShortcut("-", modifiers: .command)
                    Button("Fit") { zoom = nil }.keyboardShortcut("0", modifiers: .command)
                }
                .opacity(0).accessibilityHidden(true)
            }
            .animation(reduceMotion ? nil : .default, value: zoom)
        }
    }
}

/// A PDF's pages as bitmaps from the preview service: the first 500, at
/// the zoom asked. Nothing in a page is clickable.
struct PDFPages: View {
    let team: TeamController
    let data: Data
    let pages: Int
    @State private var zoom: CGFloat = 1

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                ForEach(0 ..< pages, id: \.self) { i in
                    PDFPageView(team: team, data: data, index: i, pages: pages, width: 700 * zoom)
                }
            }
            .padding(16)
        }
        .background {
            Group {
                Button("Zoom In") { zoom = min(zoom * 1.25, 4) }.keyboardShortcut("+", modifiers: .command)
                Button("Zoom Out") { zoom = max(zoom / 1.25, 0.25) }.keyboardShortcut("-", modifiers: .command)
                Button("Actual Size") { zoom = 1 }.keyboardShortcut("0", modifiers: .command)
            }
            .opacity(0).accessibilityHidden(true)
        }
    }
}

struct PDFPageView: View {
    let team: TeamController
    let data: Data
    let index: Int
    let pages: Int
    let width: CGFloat
    @State private var image: NSImage?
    @State private var failed = false

    var body: some View {
        Group {
            if let image {
                Image(nsImage: image).resizable().aspectRatio(contentMode: .fit).frame(width: width)
                    .shadow(radius: 1)
            } else if failed {
                Text(AttachmentText.couldntShow).foregroundStyle(.secondary).frame(width: width, height: 120)
            } else {
                Rectangle().fill(.quaternary).frame(width: width, height: width * 1.3)
            }
        }
        .accessibilityLabel(AttachmentText.pageLabel(index + 1, of: pages))
        .task(id: Int(width)) {
            let px = Int(width * (NSScreen.main?.backingScaleFactor ?? 2))
            if let png = try? await team.previewer.pdfPage(data, index: index, maxPixel: px), let img = NSImage(data: png) {
                image = img
            } else {
                failed = image == nil
            }
        }
    }
}

/// Markdown, drawn by Portenv's own views from the parsed blocks: no HTML,
/// no CSS, no fonts or images from the file. A link shows its text with its
/// URL beside it, and a click goes through the link rules.
struct MarkdownBlocks: View {
    let blocks: [AttachmentMarkdown.Block]
    @State private var confirm: (URL, String)?
    @State private var refused: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, b in block(b) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
        .environment(\.openURL, OpenURLAction { url in
            switch AttachmentLink.policy(url.absoluteString) {
            case .open(let u): NSWorkspace.shared.open(u)
            case .confirm(let u, let shown): confirm = (u, shown)
            case .refuse: refused = url.absoluteString
            }
            return .handled
        })
        .alert(confirm.map { AttachmentText.insecureLink($0.1) } ?? "", isPresented: Binding(get: { confirm != nil }, set: { if !$0 { confirm = nil } })) {
            Button("Cancel", role: .cancel) { confirm = nil }.keyboardShortcut(.defaultAction)
            Button("Open") {
                if let u = confirm?.0 { NSWorkspace.shared.open(u) }
                confirm = nil
            }
        }
        .alert(AttachmentText.linkRefused, isPresented: Binding(get: { refused != nil }, set: { if !$0 { refused = nil } })) {
            Button(AttachmentText.copyLink) {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(refused ?? "", forType: .string)
                refused = nil
            }
            Button("OK", role: .cancel) { refused = nil }
        } message: {
            Text(verbatim: refused ?? "")
        }
    }

    @ViewBuilder private func block(_ b: AttachmentMarkdown.Block) -> some View {
        switch b {
        case .heading(let level, let i):
            Text(Self.text(i)).font(.system(size: [0, 22, 18, 15, 14, 13, 13][min(level, 6)], weight: .bold))
                .accessibilityAddTraits(.isHeader)
        case .paragraph(let i): Text(Self.text(i)).font(.system(size: 13)).fixedSize(horizontal: false, vertical: true)
        case .listItem(let depth, let marker, let i):
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(verbatim: marker).font(.system(size: 13)).foregroundStyle(.secondary)
                Text(Self.text(i)).font(.system(size: 13)).fixedSize(horizontal: false, vertical: true)
            }
            .padding(.leading, CGFloat(depth - 1) * 16)
        case .quote(let i):
            Text(Self.text(i)).font(.system(size: 13)).foregroundStyle(.secondary)
                .padding(.leading, 10)
                .overlay(alignment: .leading) { Rectangle().fill(.tertiary).frame(width: 3) }
        case .code(let t):
            Text(verbatim: AttachmentText.visible(t)).font(.system(size: 12, design: .monospaced))
                .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 6).fill(.quaternary))
        case .table(let rows):
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 4) {
                ForEach(Array(rows.enumerated()), id: \.offset) { r, cells in
                    GridRow {
                        ForEach(Array(cells.enumerated()), id: \.offset) { _, c in
                            Text(Self.text(c)).font(.system(size: 12, weight: r == 0 ? .semibold : .regular))
                        }
                    }
                }
            }
        case .rule: Divider()
        case .plain(let t):
            Text(verbatim: AttachmentText.visible(t)).font(.system(size: 12, design: .monospaced)).fixedSize(horizontal: false, vertical: true)
        }
    }

    /// The inlines as one text: emphasis, strong, code and strikethrough;
    /// a link's text clickable with its URL beside it in secondary text; an
    /// image as words, never loaded.
    static func text(_ inlines: [AttachmentMarkdown.Inline]) -> AttributedString {
        var out = AttributedString()
        for i in inlines {
            switch i {
            case .text(let t, let styles):
                var s = AttributedString(AttachmentText.visible(t))
                var intent: InlinePresentationIntent = []
                if styles.contains(.emphasis) { intent.insert(.emphasized) }
                if styles.contains(.strong) { intent.insert(.stronglyEmphasized) }
                if styles.contains(.strikethrough) { intent.insert(.strikethrough) }
                s.inlinePresentationIntent = intent
                out += s
            case .code(let t):
                var s = AttributedString(AttachmentText.visible(t))
                s.inlinePresentationIntent = .code
                out += s
            case .link(let t, let u):
                var s = AttributedString(AttachmentText.visible(t))
                s.link = URL(string: u)
                out += s
                var url = AttributedString(" (\(AttachmentText.visible(u)))")
                url.foregroundColor = .secondary
                out += url
            case .image(let alt, let u):
                var s = AttributedString("Image: \(AttachmentText.visible(alt)) (\(AttachmentText.visible(u)))")
                s.foregroundColor = .secondary
                out += s
            }
        }
        return out
    }
}
