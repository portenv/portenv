// SPDX-License-Identifier: Apache-2.0

import CoreServices
import Foundation
import PortenvPreviewCore

// Attachments on agents' messages and decisions, and the owner's files for
// agents (ADR 0030). The board carries each attachment's record; the bytes
// stay in this Mac's store, which only portenvd reads (ReadAttachment). An
// attachment's contents are decoded only in PortenvPreview.xpc; everything
// an agent named (a file name) is shown as plain text.

/// One attachment's record, as the board has it.
public struct TeamAttachment: Equatable, Hashable, Sendable, Identifiable {
    /// At most 5 on one message or decision (ADR 0030 point 1).
    public static let maxPerLine = 5

    /// A ULID: the store's id.
    public var id: String
    /// Plain text on one line, as portenvd cleaned it, cleaned again here.
    public var name: String
    public var mediaType: String
    public var size: Int64
    public var sha256: String
    /// Images only, from the header.
    public var width: Int
    public var height: Int
    /// Where an agent's copy landed in its home; empty for the owner.
    public var inboxPath: String

    public init(id: String, name: String, mediaType: String, size: Int64 = 0, sha256: String = "", width: Int = 0, height: Int = 0,
                inboxPath: String = "") {
        self.id = id
        self.name = TeamText.line(name, max: 240)
        self.mediaType = mediaType
        self.size = max(0, size)
        self.sha256 = sha256
        self.width = max(0, width)
        self.height = max(0, height)
        self.inboxPath = TeamText.line(inboxPath, max: 1024)
    }

    public var kind: AttachmentKind { AttachmentKind(mediaType: mediaType) }
}

/// What an attachment is, from portenvd's type check (never its name).
public enum AttachmentKind: Equatable, Sendable {
    case png, jpeg, gif, webp, pdf, markdown, text, docx, other

    public init(mediaType: String) {
        self = switch mediaType {
        case "image/png": .png
        case "image/jpeg": .jpeg
        case "image/gif": .gif
        case "image/webp": .webp
        case "application/pdf": .pdf
        case "text/markdown": .markdown
        case "text/plain": .text
        case "application/vnd.openxmlformats-officedocument.wordprocessingml.document": .docx
        default: .other
        }
    }

    public var isImage: Bool { [.png, .jpeg, .gif, .webp].contains(self) }
    /// A .docx is never previewed: a chip with Save to Mac… only.
    public var previewable: Bool { self != .docx && self != .other }

    public var typeName: String {
        switch self {
        case .png: "PNG image"
        case .jpeg: "JPEG image"
        case .gif: "GIF image"
        case .webp: "WebP image"
        case .pdf: "PDF"
        case .markdown: "Markdown"
        case .text: "Text"
        case .docx: "Word document"
        case .other: "File"
        }
    }
}

/// Whether this Mac's store has an attachment's bytes.
public enum AttachmentAvailability: Equatable, Sendable {
    /// Not asked yet.
    case unknown
    case here
    /// The store doesn't have it: the workspace came from another Mac, or
    /// another Mac took the message.
    case elsewhere
    /// The owner removed it from this Mac; its record stays.
    case removed
}

/// A notification for an attachment pushed to the owner (ADR 0030 point
/// 9a): one on a message to the owner or on a decision.
public struct AttachmentNotice: Equatable, Sendable {
    public var agent: String
    public var title: String
    public var body: String
    public init(agent: String, title: String, body: String) {
        self.agent = agent
        self.title = title
        self.body = body
    }
}

/// The line under the tab bar after a drop typed nothing: where the copy
/// is, with Copy Path.
public struct DropNotice: Equatable, Sendable {
    public var line: String
    /// The absolute path in the workspace, for Copy Path.
    public var path: String
    public init(line: String, path: String) {
        self.line = line
        self.path = path
    }
}

/// One of the owner's files on its way to agents: its base name and bytes.
/// The Mac path never leaves the app.
public struct OutgoingAttachment: Equatable, Sendable {
    public var name: String
    public var data: Data
    public init(name: String, data: Data) {
        self.name = name
        self.data = data
    }
}

/// How a dropped file's path reached the agent's pane.
public enum InsertHow: Equatable, Sendable { case typed, pasted, none }

/// SendAttachments' answer.
public struct SentAttachments: Equatable, Sendable {
    public var messages: [TeamMessage]
    public var refused: [String]
    public var inserted: InsertHow
    public var insertedPath: String
    public init(messages: [TeamMessage], refused: [String], inserted: InsertHow, insertedPath: String) {
        self.messages = messages
        self.refused = refused
        self.inserted = inserted
        self.insertedPath = insertedPath
    }
}

/// One row of the Attachments panel: an attachment on one board line.
public struct AttachmentEntry: Equatable, Sendable, Identifiable {
    public var attachment: TeamAttachment
    /// The message's or decision's id.
    public var lineID: String
    public var from: String
    public var to: String
    public var at: Date?
    public var isDecision: Bool

    public var id: String { lineID + "/" + attachment.id }

    /// "Builder → Reviewer".
    public func route(team: [TeamAgent]) -> String {
        "\(TeamMessageText.name(from, team: team)) \u{2192} \(TeamMessageText.name(to, team: team))"
    }
}

extension TeamState {
    /// Every attachment on the board, newest first (ADR 0030 point 5, R-0381):
    /// what the owner always sees, whatever is muted. For one agent: what
    /// it sent and received.
    public func attachmentEntries(filter agent: String? = nil) -> [AttachmentEntry] {
        var out: [AttachmentEntry] = []
        for m in messages where agent == nil || m.from == agent || m.to == agent {
            for a in m.attachments {
                out.append(AttachmentEntry(attachment: a, lineID: m.id, from: m.from, to: m.to, at: m.createdAt, isDecision: false))
            }
        }
        for d in decisions where agent == nil || d.from == agent {
            for a in d.attachments {
                out.append(AttachmentEntry(attachment: a, lineID: d.id, from: d.from, to: TeamText.owner, at: d.createdAt, isDecision: true))
            }
        }
        return out.enumerated().sorted { l, r in
            let a = l.element.at ?? .distantPast, b = r.element.at ?? .distantPast
            return a != b ? a > b : l.offset > r.offset
        }.map(\.element)
    }
}

/// The words for attachments (GUIDELINES 6.2).
public enum AttachmentText {
    public static let notOnThisMac = "This attachment isn\u{2019}t on this Mac."
    public static let couldntShow = PreviewError.cantShow.line
    public static let saveToMac = "Save to Mac\u{2026}"
    public static let removeFromMac = "Remove from this Mac"
    public static let attach = "Attach\u{2026}"
    public static let copyPath = "Copy Path"
    public static let sendToWholeTeam = "Send to whole team"
    public static let showAttachments = "Show Attachments"
    public static let noAttachments = "No attachments yet."
    public static let linkRefused = "Portenv doesn\u{2019}t open file or app links from attachments."
    public static let copyLink = "Copy Link"
    public static let removeConfirm = "Remove this attachment from this Mac?"
    public static let removeDetail = "Its message stays, and the chip reads \u{201C}removed\u{201D}. Agents\u{2019} own copies stay in their homes."
    public static let folders = "Folders can\u{2019}t be attached yet. Drop files."
    public static let tooMany = "Attach at most \(TeamAttachment.maxPerLine) files at once."
    public static let maxBytes = 20 * 1024 * 1024

    public static func size(_ a: TeamAttachment) -> String { TeamText.size(a.size, locale: Locale(identifier: "en_US")) }

    /// "redesign.md · Markdown · 14 KB"; greyed, "redesign.md · on another
    /// Mac"; removed, "redesign.md · removed".
    public static func chip(_ a: TeamAttachment, availability: AttachmentAvailability = .here) -> String {
        switch availability {
        case .elsewhere: "\(a.name) \u{00B7} on another Mac"
        case .removed: "\(a.name) \u{00B7} removed"
        case .here, .unknown: "\(a.name) \u{00B7} \(a.kind.typeName) \u{00B7} \(size(a))"
        }
    }

    /// The chip's symbol when there's no thumbnail.
    public static func symbol(_ a: TeamAttachment) -> String {
        switch a.kind {
        case .markdown: "doc.richtext"
        case .text: "doc.text"
        case .png, .jpeg, .gif, .webp: "photo"
        case .pdf, .docx, .other: "doc"
        }
    }

    /// VoiceOver: one element per chip (GUIDELINES 12).
    public static func spoken(_ a: TeamAttachment, from: String, availability: AttachmentAvailability = .here) -> String {
        let head = "Attachment: \(a.name), \(a.kind.typeName), \(size(a)), from \(from)"
        switch availability {
        case .elsewhere: return head + ", on another Mac, can\u{2019}t be shown."
        case .removed: return head + ", removed from this Mac."
        case .here, .unknown:
            return head + (a.kind.previewable ? ". Opens a preview." : ". Save it to your Mac to open it.")
        }
    }

    /// The preview's header: "redesign.md · from Builder · 14:28 · 14 KB".
    public static func header(_ a: TeamAttachment, from: String, at: Date?, timeZone: TimeZone = .current) -> String {
        let time = TeamMessageText.time(at, timeZone: timeZone)
        return ([a.name, "from \(from)"] + (time.isEmpty ? [] : [time]) + [size(a)]).joined(separator: " \u{00B7} ")
    }

    public static func saveLabel(_ a: TeamAttachment) -> String { "Save \(a.name) to your Mac" }
    public static func pageLabel(_ page: Int, of count: Int) -> String { "Page \(page) of \(count)" }
    public static func panelHeading(_ n: Int) -> String { "Attachments \u{00B7} \(n)" }
    public static func mute(_ agent: String, muted: Bool) -> String { (muted ? "Unmute" : "Mute") + " Attachments from \(agent)" }
    public static func insecureLink(_ shown: String) -> String { "Open this link? \(shown) isn\u{2019}t a secure link." }
    public static func secureLink(_ shown: String) -> String { "Open this link? \(shown) opens in your browser." }

    /// What's attached, as a notification's title says it.
    public static func attached(_ list: [TeamAttachment]) -> String {
        list.count == 1 ? "attached \(list[0].name)" : "attached \(list.count) files"
    }

    /// "SPEC.md is in Builder's inbox: ~/inbox/owner/…/SPEC.md".
    public static func inbox(_ what: String, agent: String, path: String) -> String {
        "\(what) \(what.hasSuffix(" files") ? "are" : "is") in \(agent)\u{2019}s inbox: \(path)"
    }

    public static func typeRefused(_ name: String) -> String {
        "\(name) isn\u{2019}t a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."
    }

    public static func tooLarge(_ name: String, bytes: Int) -> String {
        "\(name) is \(TeamText.size(Int64(bytes), locale: Locale(identifier: "en_US"))); an attachment can be at most 20 MB. Make it smaller and attach it again."
    }

    /// Plain text as the preview shows it: tabs and line breaks kept; every
    /// other control character, escape and bidi override shown as a visible
    /// escape (`\u{202E}`), never acted on.
    public static func visible(_ s: String) -> String {
        var out = ""
        for u in s.unicodeScalars {
            switch u.value {
            case 0x0A, 0x09: out.unicodeScalars.append(u)
            case 0x00 ... 0x1F, 0x7F ... 0x9F, 0x061C, 0x200B ... 0x200F, 0x202A ... 0x202E, 0x2060 ... 0x2069, 0xFEFF:
                out += "\\u{" + String(format: u.value < 0x100 ? "%02X" : "%04X", u.value) + "}"
            default: out.unicodeScalars.append(u)
            }
        }
        return out
    }
}

/// What a click on a link in a previewed attachment does (ADR 0030 point
/// 5, R-0393): every link asks first, with the whole URL and its host in
/// ASCII: https says it opens in the browser, http and mailto that they
/// aren't secure. file, relative and every other scheme never open. Nothing
/// in an attachment opens on a single click.
public enum AttachmentLink: Equatable, Sendable {
    /// Ask with this question, Cancel the default; open only on Open.
    case confirm(URL, question: String)
    case refuse

    public static func policy(_ link: String) -> AttachmentLink {
        // URL's parser writes an international host in its ASCII form.
        guard let url = URL(string: link), let scheme = url.scheme?.lowercased() else { return .refuse }
        let shown = url.absoluteString
        switch scheme {
        case "https" where url.host?.isEmpty == false: return .confirm(url, question: AttachmentText.secureLink(shown))
        case "http" where url.host?.isEmpty == false: return .confirm(url, question: AttachmentText.insecureLink(shown))
        case "mailto": return .confirm(url, question: AttachmentText.insecureLink(shown))
        default: return .refuse
        }
    }
}

/// The owner's files, checked in the app before anything is sent (ADR 0030
/// point 8): no folders or links, at most 5, at most 20 MB each, and only
/// point 1's types, from the content. portenvd checks them all again.
public enum OutgoingAttachments {
    /// The files as they'll go, or the one line that refuses them all.
    public static func prepare(_ urls: [URL]) -> (files: [OutgoingAttachment], refusal: String?) {
        guard urls.count <= TeamAttachment.maxPerLine else { return ([], AttachmentText.tooMany) }
        var checked: [(URL, String)] = []
        for url in urls {
            let name = url.lastPathComponent
            var st = stat()
            guard lstat(url.path, &st) == 0 else { return ([], "Portenv couldn\u{2019}t read \(name). Check it\u{2019}s still there and attach it again.") }
            switch st.st_mode & S_IFMT {
            case S_IFDIR: return ([], AttachmentText.folders)
            case S_IFLNK: return ([], "\(name) is a link. Attach the file itself.")
            case S_IFREG: checked.append((url, name))
            default: return ([], AttachmentText.typeRefused(name))
            }
        }
        var out: [OutgoingAttachment] = []
        for (url, name) in checked {
            guard let h = try? FileHandle(forReadingFrom: url) else {
                return ([], "Portenv couldn\u{2019}t read \(name). Check it\u{2019}s still there and attach it again.")
            }
            defer { try? h.close() }
            let data = (try? h.read(upToCount: AttachmentText.maxBytes + 1)) ?? Data()
            if data.count > AttachmentText.maxBytes {
                let size = ((try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? Int) ?? data.count
                return ([], AttachmentText.tooLarge(name, bytes: size))
            }
            guard allowed(data, name: name) else { return ([], AttachmentText.typeRefused(name)) }
            out.append(OutgoingAttachment(name: name, data: data))
        }
        return (out, nil)
    }

    /// A pasted image, as a PNG.
    public static func pasted(png: Data) -> OutgoingAttachment { OutgoingAttachment(name: "Pasted image.png", data: png) }

    /// Point 1's inbound types, from the content: images and PDFs by their
    /// signature, Word by its ZIP signature and name, Markdown and text as
    /// valid UTF-8 named .md or .txt. portenvd's check is the real one.
    static func allowed(_ d: Data, name: String) -> Bool {
        let ext = (name as NSString).pathExtension.lowercased()
        let head = d.prefix(1024)
        func starts(_ bytes: [UInt8]) -> Bool { d.starts(with: bytes) }
        if starts([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]) || starts([0xFF, 0xD8, 0xFF]) || starts(Array("GIF87a".utf8))
            || starts(Array("GIF89a".utf8)) || (starts(Array("RIFF".utf8)) && d.count >= 12 && d[d.startIndex + 8 ..< d.startIndex + 12] == Data("WEBP".utf8)) {
            return true
        }
        if head.range(of: Data("%PDF-1.".utf8)) != nil || head.range(of: Data("%PDF-2.".utf8)) != nil {
            return d.suffix(1024).range(of: Data("%%EOF".utf8)) != nil
        }
        if starts([0x50, 0x4B, 0x03, 0x04]) { return ext == "docx" }
        guard ["md", "markdown", "txt"].contains(ext), let s = String(data: d, encoding: .utf8) else { return false }
        return !s.unicodeScalars.contains { ($0.value < 0x20 && ![0x09, 0x0A, 0x0D].contains($0.value)) || (0x7F ... 0x9F).contains($0.value) }
    }
}

/// Save to Mac… (ADR 0030 point 5): only where the person chose in the save
/// panel, proposing the cleaned name in Downloads; the saved file is
/// quarantined, so the apps that open it treat it as downloaded. Never
/// opened afterwards.
public enum AttachmentSave {
    public static var proposedDirectory: URL {
        FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask).first
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Downloads")
    }

    /// Writes data where `choose` (the panel) says; nil when cancelled.
    @MainActor
    public static func save(_ data: Data, name: String, choose: (String) async -> URL?) async throws -> URL? {
        guard let url = await choose(name) else { return nil }
        try data.write(to: url, options: .atomic)
        try quarantine(url)
        return url
    }

    /// com.apple.quarantine, from Portenv, as a download.
    public static func quarantine(_ url: URL) throws {
        var values = URLResourceValues()
        values.quarantineProperties = [
            kLSQuarantineAgentNameKey as String: "Portenv",
            kLSQuarantineTypeKey as String: kLSQuarantineTypeOtherDownload as String,
        ]
        var u = url
        try u.setResourceValues(values)
    }
}

/// The preview service couldn't show a file: its line.
public struct PreviewFailure: Error, Equatable, Sendable {
    public let line: String
    public init(_ line: String) { self.line = line }
}

/// The preview service, as the app asks it: bytes in, bounded PNGs out.
public protocol AttachmentPreviewing: Sendable {
    func thumbnail(_ data: Data, maxPixel: Int) async throws -> Data
    func pdfPageCount(_ data: Data) async throws -> Int
    func pdfPage(_ data: Data, index: Int, maxPixel: Int) async throws -> Data
}

/// PortenvPreview.xpc, one connection per call, each with a deadline: a
/// crash, a hang or a refusal ends that call with a line, never the app.
public final class XPCPreviewer: AttachmentPreviewing, @unchecked Sendable {
    /// The service's bundle identifier, inside the app's XPCServices.
    public static let serviceName = "com.portenv.Portenv.Preview"
    let deadline: Duration
    let connect: @Sendable () -> NSXPCConnection

    public init(deadline: Duration = .seconds(10), connect: @escaping @Sendable () -> NSXPCConnection = { NSXPCConnection(serviceName: XPCPreviewer.serviceName) }) {
        self.deadline = deadline
        self.connect = connect
    }

    public func thumbnail(_ data: Data, maxPixel: Int) async throws -> Data {
        try await call { p, reply in p.thumbnail(data, maxPixel: maxPixel) { d, line in reply(d, line) } }
    }

    public func pdfPageCount(_ data: Data) async throws -> Int {
        try await call { p, reply in p.pdfPageCount(data) { n, line in reply(line == nil ? n : nil, line) } }
    }

    public func pdfPage(_ data: Data, index: Int, maxPixel: Int) async throws -> Data {
        try await call { p, reply in p.pdfPage(data, index: index, maxPixel: maxPixel) { d, line in reply(d, line) } }
    }

    /// Resumes its continuation once, whichever comes first.
    private final class Once<T: Sendable>: @unchecked Sendable {
        private let lock = NSLock()
        private var cont: CheckedContinuation<T, Error>?
        init(_ c: CheckedContinuation<T, Error>) { cont = c }
        func finish(_ r: Result<T, Error>) {
            let c: CheckedContinuation<T, Error>? = lock.withLock {
                defer { cont = nil }
                return cont
            }
            c?.resume(with: r)
        }
    }

    private final class Conn: @unchecked Sendable {
        let c: NSXPCConnection
        init(_ c: NSXPCConnection) { self.c = c }
    }

    private func call<T: Sendable>(_ body: @escaping @Sendable (PreviewXPC, @escaping @Sendable (T?, String?) -> Void) -> Void) async throws -> T {
        let conn = Conn(connect())
        conn.c.remoteObjectInterface = NSXPCInterface(with: PreviewXPC.self)
        let deadline = self.deadline
        return try await withThrowingTaskGroup(of: T.self) { group in
            group.addTask {
                try await withCheckedThrowingContinuation { (cont: CheckedContinuation<T, Error>) in
                    let once = Once(cont)
                    let failed: @Sendable (String?) -> Void = { once.finish(.failure(PreviewFailure($0 ?? AttachmentText.couldntShow))) }
                    conn.c.interruptionHandler = { failed(nil) }
                    conn.c.invalidationHandler = { failed(nil) }
                    conn.c.resume()
                    guard let proxy = conn.c.remoteObjectProxyWithErrorHandler({ _ in failed(nil) }) as? PreviewXPC else {
                        failed(nil)
                        return
                    }
                    body(proxy) { value, line in
                        if let value { once.finish(.success(value)) } else { failed(line) }
                    }
                }
            }
            group.addTask {
                try await Task.sleep(for: deadline)
                // A hang: the connection goes, and the call ends with it.
                conn.c.invalidate()
                throw PreviewFailure(AttachmentText.couldntShow)
            }
            defer {
                group.cancelAll()
                conn.c.invalidate()
            }
            guard let first = try await group.next() else { throw PreviewFailure(AttachmentText.couldntShow) }
            return first
        }
    }
}
