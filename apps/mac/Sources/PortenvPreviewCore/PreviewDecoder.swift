// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import ImageIO
import PDFKit
import UniformTypeIdentifiers

// The preview service's decoding (ADR 0030 point 5): every decode of an
// attachment's contents happens here, in PortenvPreview.xpc, sandboxed with
// no network and no files. It gets bytes and gives back bounded bitmaps
// (PNG), never anything that acts: images through ImageIO, read only as a
// bounded thumbnail after their size is checked; PDFs through PDFKit, as
// pages drawn to bitmaps, with every annotation's action taken away.

/// What the service says when it can't show a file, as the line the app
/// shows.
public enum PreviewError: Error, Equatable, Sendable {
    case tooLarge(String)
    case encrypted
    case cantShow

    public var line: String {
        switch self {
        case .tooLarge(let l): l
        case .encrypted: "This PDF has a password. Save it to your Mac to open it."
        case .cantShow: "Portenv couldn\u{2019}t show this file."
        }
    }
}

/// The service's XPC protocol: bytes in, a PNG (or a page count) and an
/// error line out.
@objc public protocol PreviewXPC {
    func thumbnail(_ data: Data, maxPixel: Int, reply: @escaping (Data?, String?) -> Void)
    func pdfPageCount(_ data: Data, reply: @escaping (Int, String?) -> Void)
    func pdfPage(_ data: Data, index: Int, maxPixel: Int, reply: @escaping (Data?, String?) -> Void)
}

public enum PreviewDecoder {
    /// ADR 0030 point 1's limits.
    public static let maxSide = 16384
    public static let maxPixels = 100_000_000
    public static let maxPages = 500
    /// The largest bitmap the service ever makes, a side.
    public static let maxBitmap = 8192

    public static func clamp(_ pixels: Int) -> Int { min(max(pixels, 16), maxBitmap) }

    public static func shownPages(_ count: Int) -> Int { min(count, maxPages) }

    static func thousands(_ n: Int) -> String {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        f.locale = Locale(identifier: "en_US")
        return f.string(from: NSNumber(value: n)) ?? String(n)
    }

    /// An image's thumbnail, at most maxPixel a side, as PNG. Its size is
    /// read from its header and checked before anything is decoded; only
    /// the first frame of an animation is shown.
    public static func thumbnail(_ data: Data, maxPixel: Int) throws(PreviewError) -> Data {
        let noCache = [kCGImageSourceShouldCache: false] as CFDictionary
        guard let src = CGImageSourceCreateWithData(data as CFData, noCache), CGImageSourceGetCount(src) > 0,
              let props = CGImageSourceCopyPropertiesAtIndex(src, 0, noCache) as? [CFString: Any],
              let w = props[kCGImagePropertyPixelWidth] as? Int, let h = props[kCGImagePropertyPixelHeight] as? Int, w > 0, h > 0
        else { throw .cantShow }
        if w > maxSide {
            throw .tooLarge("This image is \(thousands(w)) px wide; Portenv shows images up to \(thousands(maxSide)) px a side.")
        }
        if h > maxSide {
            throw .tooLarge("This image is \(thousands(h)) px tall; Portenv shows images up to \(thousands(maxSide)) px a side.")
        }
        if w * h > maxPixels {
            throw .tooLarge("This image is \(w * h / 1_000_000) megapixels; Portenv shows images up to \(maxPixels / 1_000_000) megapixels.")
        }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: false,
            kCGImageSourceShouldCache: false,
            kCGImageSourceThumbnailMaxPixelSize: clamp(maxPixel),
        ]
        guard let image = CGImageSourceCreateThumbnailAtIndex(src, 0, options as CFDictionary) else { throw .cantShow }
        return try png(image)
    }

    static func png(_ image: CGImage) throws(PreviewError) -> Data {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, UTType.png.identifier as CFString, 1, nil) else { throw .cantShow }
        CGImageDestinationAddImage(dest, image, nil)
        guard CGImageDestinationFinalize(dest) else { throw .cantShow }
        return out as Data
    }

    /// A PDF, loaded for pages only: an encrypted one is refused (never
    /// asked for a password), and every annotation loses its action, so
    /// nothing in it is ever followed, launched or submitted.
    public static func document(_ data: Data) throws(PreviewError) -> PDFDocument {
        guard let doc = PDFDocument(data: data) else { throw .cantShow }
        if doc.isEncrypted || doc.isLocked { throw .encrypted }
        for i in 0 ..< shownPages(doc.pageCount) {
            for a in doc.page(at: i)?.annotations ?? [] {
                a.action = nil
            }
        }
        return doc
    }

    public static func pdfPageCount(_ data: Data) throws(PreviewError) -> Int {
        shownPages(try document(data).pageCount)
    }

    /// One page drawn to a bitmap, at most maxPixel a side, as PNG.
    public static func pdfPage(_ data: Data, index: Int, maxPixel: Int) throws(PreviewError) -> Data {
        let doc = try document(data)
        guard index >= 0, index < shownPages(doc.pageCount), let page = doc.page(at: index) else { throw .cantShow }
        let bounds = page.bounds(for: .cropBox)
        guard bounds.width > 0, bounds.height > 0 else { throw .cantShow }
        let side = CGFloat(clamp(maxPixel))
        let scale = side / max(bounds.width, bounds.height)
        let size = CGSize(width: max(1, (bounds.width * scale).rounded()), height: max(1, (bounds.height * scale).rounded()))
        let image = page.thumbnail(of: size, for: .cropBox)
        guard let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else { throw .cantShow }
        return try png(cg)
    }
}

/// The service's side of the protocol: each call decoded here, its answer
/// a PNG or a line.
public final class PreviewService: NSObject, PreviewXPC {
    public func thumbnail(_ data: Data, maxPixel: Int, reply: @escaping (Data?, String?) -> Void) {
        do { reply(try PreviewDecoder.thumbnail(data, maxPixel: maxPixel), nil) } catch { reply(nil, error.line) }
    }

    public func pdfPageCount(_ data: Data, reply: @escaping (Int, String?) -> Void) {
        do { reply(try PreviewDecoder.pdfPageCount(data), nil) } catch { reply(0, error.line) }
    }

    public func pdfPage(_ data: Data, index: Int, maxPixel: Int, reply: @escaping (Data?, String?) -> Void) {
        do { reply(try PreviewDecoder.pdfPage(data, index: index, maxPixel: maxPixel), nil) } catch { reply(nil, error.line) }
    }
}
