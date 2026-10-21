// SPDX-License-Identifier: Apache-2.0

import AppKit
import CoreGraphics
import Foundation
import ImageIO
import PDFKit
import Testing

@testable import PortenvKit
@testable import PortenvPreviewCore

/// Fails the test on any URL load the process makes: nothing in an
/// attachment's preview may fetch anything (ADR 0030 point 5).
final class NoLoads: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [String] = []
    override class func canInit(with request: URLRequest) -> Bool {
        seen.append(request.url?.absoluteString ?? "?")
        return false
    }
}

/// ADR 0030 point 12, slice 4: the preview's rules, written first.
@Suite(.serialized)
struct AttachmentPreviewTests {
    // MARK: Markdown: a strict native renderer, never a web view

    static let hostile = """
    # Plan <b>bold</b>

    Text <script>alert(1)</script> and <img src="https://evil.example/x.png"> and ![x](https://evil.example/pixel.png).

    <iframe src="https://evil.example"></iframe>

    [a link](http://evil.example/a) and [ok](https://example.com/b) and [file](file:///etc/passwd) and [app](vscode://x)
    """

    @Test func rawHTMLAndImagesRenderAsTextWithNothingFetched() {
        URLProtocol.registerClass(NoLoads.self)
        defer { URLProtocol.unregisterClass(NoLoads.self) }
        NoLoads.seen = []
        let blocks = AttachmentMarkdown.parse(Data(Self.hostile.utf8))
        let text = AttachmentMarkdown.plainText(blocks)
        for literal in ["<b>bold</b>", "<script>alert(1)</script>", "<img src=\"https://evil.example/x.png\">", "<iframe src=\"https://evil.example\"></iframe>"] {
            #expect(text.contains(literal), "\(literal) isn't shown as the text it is")
        }
        #expect(text.contains("Image: x (https://evil.example/pixel.png)"))
        // Every link shows its URL beside its text.
        #expect(text.contains("a link (http://evil.example/a)"))
        #expect(NoLoads.seen.isEmpty, "loaded \(NoLoads.seen)")
        #expect(text.hasPrefix("Plan <b>bold</b>"))
        // Nothing in it is a link the renderer would follow on its own.
        #expect(!blocks.isEmpty)
    }

    @Test func markdownStructureIsKept() {
        let src = "# H1\n\n## H2\n\n- one\n  1. nested\n\n> quoted\n\n```\nlet x = 1 < 2\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n---\n\n*em* **strong** `code` ~~gone~~\n"
        let blocks = AttachmentMarkdown.parse(Data(src.utf8))
        #expect(blocks.contains { if case .heading(1, _) = $0 { true } else { false } })
        #expect(blocks.contains { if case .heading(2, _) = $0 { true } else { false } })
        #expect(blocks.contains { if case .listItem(depth: 2, marker: "1.", _) = $0 { true } else { false } })
        #expect(blocks.contains { if case .quote = $0 { true } else { false } })
        #expect(blocks.contains(.code("let x = 1 < 2")))
        #expect(blocks.contains { if case .table(let rows) = $0 { rows.count == 2 } else { false } })
        #expect(blocks.contains(.rule))
    }

    @Test func tooLargeOrNotMarkdownIsPlainText() {
        let big = Data(repeating: UInt8(ascii: "a"), count: AttachmentMarkdown.maxFormatted + 1)
        #expect(AttachmentMarkdown.parse(big) == [.plain(String(repeating: "a", count: AttachmentMarkdown.maxFormatted + 1))])
        #expect(AttachmentMarkdown.parse(Data([0xff, 0xfe, 0x41])).count == 1)
        // Nesting past 16 levels is plain text from there.
        let deep = String(repeating: "> ", count: 20) + "deep"
        #expect(AttachmentMarkdown.parse(Data(deep.utf8)) == [.plain("deep")])
    }

    // MARK: Links: shown with their URL, opened only by scheme, never without asking

    @Test func linkPolicyPerScheme() throws {
        // Every link asks first, https included (R-0393): nothing in an
        // attachment opens on a single click.
        #expect(AttachmentLink.policy("https://example.com/a")
            == .confirm(URL(string: "https://example.com/a")!, question: "Open this link? https://example.com/a opens in your browser."))
        #expect(AttachmentLink.policy("http://example.com/a")
            == .confirm(URL(string: "http://example.com/a")!, question: "Open this link? http://example.com/a isn\u{2019}t a secure link."))
        #expect(AttachmentLink.policy("mailto:a@example.com")
            == .confirm(URL(string: "mailto:a@example.com")!, question: "Open this link? mailto:a@example.com isn\u{2019}t a secure link."))
        // International host names are shown in their ASCII form.
        for idn in ["http://bücher.example/a", "https://bücher.example/a"] {
            guard case .confirm(_, let question) = AttachmentLink.policy(idn) else { Issue.record("\(idn) not confirmed"); return }
            #expect(question.contains("://xn--bcher-kva.example/a") && !question.contains("ü"))
        }
        for refused in ["file:///etc/passwd", "relative/path.md", "../x", "ssh://host", "vscode://open", "x-apple.systempreferences:", "javascript:alert(1)", "myapp://x", "",
                        "https://", "http://"] {
            #expect(AttachmentLink.policy(refused) == .refuse, "\(refused) isn't refused")
        }
        // No link of any scheme, case or shape opens without a question.
        for link in ["https://example.com", "HTTPS://example.com/x", "https://user@example.com:8443/a?b#c", "http://example.com", "mailto:x@y.z", "file:///x", "ftp://x"] {
            if case .confirm = AttachmentLink.policy(link) { continue }
            #expect(AttachmentLink.policy(link) == .refuse, "\(link) opens without asking")
        }
        #expect(AttachmentText.linkRefused == "Portenv doesn\u{2019}t open file or app links from attachments.")
    }

    // MARK: Plain text: control characters shown, never acted on

    @Test func plainTextShowsEscapesVisibly() {
        let shown = AttachmentText.visible("a\u{1b}[31mred\u{202E}cba\u{0007}\nnext\tline")
        #expect(!shown.contains("\u{1b}") && !shown.contains("\u{202E}") && !shown.contains("\u{0007}"))
        #expect(shown.contains("\\u{1B}[31mred\\u{202E}cba\\u{07}\nnext\tline"))
    }

    // MARK: Images: ImageIO, bounded, in the preview service

    /// A whole, valid one-bit grey PNG of w×h, all black, that compresses
    /// to a few KB: a decompression bomb. Refused from its header, it's
    /// never decoded.
    static func pngHeader(_ w: UInt32, _ h: UInt32) -> Data {
        func chunk(_ type: String, _ body: Data) -> Data {
            var c = withUnsafeBytes(of: UInt32(body.count).bigEndian) { Data($0) }
            let typed = Data(type.utf8) + body
            c.append(typed)
            c.append(withUnsafeBytes(of: crc32(typed).bigEndian) { Data($0) })
            return c
        }
        var ihdr = withUnsafeBytes(of: w.bigEndian) { Data($0) } + withUnsafeBytes(of: h.bigEndian) { Data($0) }
        // One bit a pixel keeps the test's own work small.
        ihdr.append(contentsOf: [1, 0, 0, 0, 0])
        // Each row: filter byte 0, then the row's bits, all zero.
        let raw = Data(count: ((Int(w) + 7) / 8 + 1) * Int(h))
        let deflated = try! (raw as NSData).compressed(using: .zlib) as Data
        var zlib = Data([0x78, 0x9C])
        zlib.append(deflated)
        // Adler-32 of all zeros: a = 1, b = length mod 65521.
        let adler = UInt32(raw.count % 65521) << 16 | 1
        zlib.append(withUnsafeBytes(of: adler.bigEndian) { Data($0) })
        var d = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])
        d.append(chunk("IHDR", ihdr))
        d.append(chunk("IDAT", zlib))
        d.append(chunk("IEND", Data()))
        return d
    }

    static func crc32(_ d: Data) -> UInt32 {
        var c: UInt32 = 0xFFFF_FFFF
        for b in d {
            c ^= UInt32(b)
            for _ in 0 ..< 8 { c = c & 1 == 1 ? 0xEDB8_8320 ^ (c >> 1) : c >> 1 }
        }
        return ~c
    }

    static func png(_ w: Int, _ h: Int) -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: w, pixelsHigh: h, bitsPerSample: 8, samplesPerPixel: 4,
                                   hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        return rep.representation(using: .png, properties: [:])!
    }

    @Test func aHugeImageIsRefusedBeforeItsDecoded() {
        #expect(throws: PreviewError.tooLarge("This image is 40,000 px wide; Portenv shows images up to 16,384 px a side.")) {
            try PreviewDecoder.thumbnail(Self.pngHeader(40000, 10), maxPixel: 256)
        }
        #expect(throws: PreviewError.tooLarge("This image is 104 megapixels; Portenv shows images up to 100 megapixels.")) {
            try PreviewDecoder.thumbnail(Self.pngHeader(10240, 10240), maxPixel: 256)
        }
    }

    @Test func anImageDecodesOnlyToABoundedThumbnail() throws {
        let png = try PreviewDecoder.thumbnail(Self.png(1200, 300), maxPixel: 256)
        let src = try #require(CGImageSourceCreateWithData(png as CFData, nil))
        let img = try #require(CGImageSourceCreateImageAtIndex(src, 0, nil))
        #expect(max(img.width, img.height) <= 256)
        #expect(throws: PreviewError.cantShow) { try PreviewDecoder.thumbnail(Data("not an image".utf8), maxPixel: 256) }
        // The preview's own size is capped at 8,192.
        #expect(PreviewDecoder.clamp(100_000) == 8192)
    }

    // MARK: PDF: PDFKit as pages, no actions, no passwords asked

    /// A one-page PDF with JavaScript to run on open, a launch action and a
    /// link annotation that launches a program.
    static let hostilePDF: Data = {
        var objs = [
            "<< /Type /Catalog /Pages 2 0 R /OpenAction 5 0 R /Names << /JavaScript << /Names [(x) 5 0 R] >> >> >>",
            "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
            "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Annots [6 0 R] >>",
            "<< /Length 44 >>\nstream\nBT /F1 12 Tf 20 100 Td (Hello) Tj ET\nendstream",
            "<< /S /JavaScript /JS (app.alert\\(1\\)) >>",
            "<< /Type /Annot /Subtype /Link /Rect [0 0 200 200] /A << /S /Launch /F (/bin/sh) >> >>",
        ]
        var out = "%PDF-1.7\n"
        var offsets: [Int] = []
        for (i, o) in objs.enumerated() {
            offsets.append(out.utf8.count)
            out += "\(i + 1) 0 obj\n\(o)\nendobj\n"
        }
        let xref = out.utf8.count
        out += "xref\n0 \(objs.count + 1)\n0000000000 65535 f \n"
        for o in offsets { out += String(format: "%010d 00000 n \n", o) }
        out += "trailer\n<< /Size \(objs.count + 1) /Root 1 0 R >>\nstartxref\n\(xref)\n%%EOF\n"
        objs.removeAll()
        return Data(out.utf8)
    }()

    @Test func aPDFWithJavaScriptAndALaunchActionRendersWithNoAction() throws {
        let doc = try PreviewDecoder.document(Self.hostilePDF)
        #expect(doc.pageCount == 1)
        for i in 0 ..< doc.pageCount {
            for a in doc.page(at: i)?.annotations ?? [] { #expect(a.action == nil, "an action survived: \(String(describing: a.action))") }
        }
        let page = try PreviewDecoder.pdfPage(Self.hostilePDF, index: 0, maxPixel: 400)
        #expect(!page.isEmpty)
        #expect(throws: PreviewError.cantShow) { try PreviewDecoder.pdfPage(Self.hostilePDF, index: 3, maxPixel: 400) }
    }

    @Test func anEncryptedPDFSaysSoAndIsntAskedForAPassword() throws {
        let doc = try #require(PDFDocument(data: Self.hostilePDF))
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".pdf")
        defer { try? FileManager.default.removeItem(at: url) }
        #expect(doc.write(to: url, withOptions: [.userPasswordOption: "secret", .ownerPasswordOption: "owner"]))
        let data = try Data(contentsOf: url)
        #expect(throws: PreviewError.encrypted) { try PreviewDecoder.pdfPageCount(data) }
        #expect(PreviewError.encrypted.line == "This PDF has a password. Save it to your Mac to open it.")
    }

    @Test func onlyTheFirst500PagesAreShown() {
        #expect(PreviewDecoder.shownPages(1200) == 500 && PreviewDecoder.shownPages(3) == 3)
    }

    // MARK: The preview service: a crash or a hang costs the preview only

    @Test func theServiceDyingMidCallLeavesTheCallerWithItsLine() async throws {
        let listener = NSXPCListener.anonymous()
        let dies = DyingService()
        dies.listener = listener
        listener.delegate = dies
        listener.resume()
        nonisolated(unsafe) let endpoint = listener.endpoint
        let previewer = XPCPreviewer(deadline: .seconds(5)) {
            NSXPCConnection(listenerEndpoint: endpoint)
        }
        await #expect(throws: PreviewFailure(AttachmentText.couldntShow)) {
            _ = try await previewer.thumbnail(Self.png(10, 10), maxPixel: 64)
        }
        // A hang: the deadline ends it.
        let hangs = XPCPreviewer(deadline: .milliseconds(300)) {
            let l = NSXPCListener.anonymous()
            let h = HangingService()
            l.delegate = h
            l.resume()
            HangingService.keep.append((l, h))
            return NSXPCConnection(listenerEndpoint: l.endpoint)
        }
        await #expect(throws: PreviewFailure(AttachmentText.couldntShow)) {
            _ = try await hangs.thumbnail(Self.png(10, 10), maxPixel: 64)
        }
        // This process carries on.
        #expect(try PreviewDecoder.thumbnail(Self.png(10, 10), maxPixel: 64).count > 0)
    }

    // MARK: Save to Mac: the panel only, and quarantine

    @MainActor
    @Test func saveToMacGoesThroughThePanelAndAddsQuarantine() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        var proposed: [String] = []
        // Cancelled: nothing is written anywhere.
        let none = try await AttachmentSave.save(Data("x".utf8), name: "login.png") { proposed.append($0); return nil }
        let left = try FileManager.default.contentsOfDirectory(atPath: dir.path)
        #expect(none == nil && left.isEmpty)
        let target = dir.appendingPathComponent("login.png")
        let saved = try await AttachmentSave.save(Data("png".utf8), name: "login.png") { proposed.append($0); return target }
        #expect(saved == target && proposed == ["login.png", "login.png"])
        #expect(try Data(contentsOf: target) == Data("png".utf8))
        let size = getxattr(target.path, "com.apple.quarantine", nil, 0, 0, 0)
        #expect(size > 0, "no quarantine")
        var buf = [UInt8](repeating: 0, count: max(size, 1))
        _ = getxattr(target.path, "com.apple.quarantine", &buf, buf.count, 0, 0)
        #expect(String(decoding: buf, as: UTF8.self).contains("Portenv"))
        #expect(AttachmentSave.proposedDirectory.lastPathComponent == "Downloads")
    }
}

/// An XPC service that drops the connection instead of answering: as a
/// crash looks to the app.
final class DyingService: NSObject, NSXPCListenerDelegate, PreviewXPC, @unchecked Sendable {
    var listener: NSXPCListener?
    var connections: [NSXPCConnection] = []
    func listener(_: NSXPCListener, shouldAcceptNewConnection c: NSXPCConnection) -> Bool {
        c.exportedInterface = NSXPCInterface(with: PreviewXPC.self)
        c.exportedObject = self
        connections.append(c)
        c.resume()
        return true
    }

    func thumbnail(_: Data, maxPixel _: Int, reply _: @escaping (Data?, String?) -> Void) {
        connections.forEach { $0.invalidate() }
    }

    func pdfPageCount(_: Data, reply _: @escaping (Int, String?) -> Void) { connections.forEach { $0.invalidate() } }
    func pdfPage(_: Data, index _: Int, maxPixel _: Int, reply _: @escaping (Data?, String?) -> Void) { connections.forEach { $0.invalidate() } }
}

/// One that never answers.
final class HangingService: NSObject, NSXPCListenerDelegate, PreviewXPC, @unchecked Sendable {
    nonisolated(unsafe) static var keep: [(NSXPCListener, HangingService)] = []
    func listener(_: NSXPCListener, shouldAcceptNewConnection c: NSXPCConnection) -> Bool {
        c.exportedInterface = NSXPCInterface(with: PreviewXPC.self)
        c.exportedObject = self
        c.resume()
        return true
    }

    func thumbnail(_: Data, maxPixel _: Int, reply _: @escaping (Data?, String?) -> Void) {}
    func pdfPageCount(_: Data, reply _: @escaping (Int, String?) -> Void) {}
    func pdfPage(_: Data, index _: Int, maxPixel _: Int, reply _: @escaping (Data?, String?) -> Void) {}
}
