// SPDX-License-Identifier: Apache-2.0

import Foundation

/// A Markdown attachment for the preview (ADR 0030 point 5): parsed by
/// Foundation, never a web view, into blocks Portenv draws with its own
/// views. Raw HTML stays the literal text it is; images are never loaded
/// ("Image: alt (url)"); every link carries its URL beside its text.
/// Larger than 2 MB, not UTF-8, or nested deeper than 16 levels, it's plain
/// text.
public enum AttachmentMarkdown {
    public static let maxFormatted = 2 * 1024 * 1024
    public static let maxDepth = 16

    public enum Style: Equatable, Sendable { case emphasis, strong, strikethrough }

    public enum Inline: Equatable, Sendable {
        case text(String, [Style])
        case code(String)
        case link(text: String, url: String)
        case image(alt: String, url: String)

        /// As plain text: a link with its URL, an image as words.
        public var plain: String {
            switch self {
            case .text(let t, _), .code(let t): t
            case .link(let t, let u): "\(t) (\(u))"
            case .image(let alt, let u): "Image: \(alt) (\(u))"
            }
        }
    }

    public enum Block: Equatable, Sendable {
        case heading(Int, [Inline])
        case paragraph([Inline])
        case listItem(depth: Int, marker: String, [Inline])
        case quote([Inline])
        case code(String)
        /// Rows of cells, the header row first.
        case table(rows: [[[Inline]]])
        case rule
        case plain(String)
    }

    public static func parse(_ data: Data) -> [Block] {
        guard let text = String(data: data, encoding: .utf8) else { return [.plain(String(decoding: data, as: UTF8.self))] }
        guard data.count <= maxFormatted,
              let doc = try? AttributedString(markdown: text, options: .init(allowsExtendedAttributes: false, interpretedSyntax: .full,
                                                                             failurePolicy: .returnPartiallyParsedIfPossible))
        else { return [.plain(text)] }
        return blocks(doc)
    }

    /// Every block's text, one per line, as plain text.
    public static func plainText(_ blocks: [Block]) -> String {
        blocks.map { b in
            switch b {
            case .heading(_, let i), .paragraph(let i), .quote(let i), .listItem(_, _, let i): i.map(\.plain).joined()
            case .code(let t), .plain(let t): t
            case .table(let rows): rows.map { $0.map { $0.map(\.plain).joined() }.joined(separator: " | ") }.joined(separator: "\n")
            case .rule: "\u{2014}"
            }
        }.joined(separator: "\n")
    }

    private static func inline(_ run: AttributedString.Runs.Run, _ text: String) -> Inline {
        if let image = run.imageURL { return .image(alt: text, url: image.absoluteString) }
        if let link = run.link { return .link(text: text, url: link.absoluteString) }
        let intent = run.inlinePresentationIntent ?? []
        if intent.contains(.code) { return .code(text) }
        var styles: [Style] = []
        if intent.contains(.emphasized) { styles.append(.emphasis) }
        if intent.contains(.stronglyEmphasized) { styles.append(.strong) }
        if intent.contains(.strikethrough) { styles.append(.strikethrough) }
        return .text(text, styles)
    }

    private static func blocks(_ doc: AttributedString) -> [Block] {
        var out: [Block] = []
        var key: [Int]?
        var inlines: [Inline] = []
        var leaf: [PresentationIntent.IntentType] = []
        var tableID: Int?
        var rows: [[[Inline]]] = []
        var rowKey: Int?

        func flushTable() {
            if tableID != nil { out.append(.table(rows: rows)) }
            tableID = nil
            rows = []
            rowKey = nil
        }
        func flush() {
            defer { inlines = [] }
            guard key != nil else { return }
            let kinds = leaf.map(\.kind)
            if leaf.count > maxDepth {
                flushTable()
                out.append(.plain(inlines.map(\.plain).joined()))
                return
            }
            if case .tableCell = kinds.first {
                let tid = leaf.first { if case .table = $0.kind { true } else { false } }?.identity
                let rid = leaf.first { switch $0.kind { case .tableRow, .tableHeaderRow: true; default: false } }?.identity
                if tid != tableID { flushTable(); tableID = tid }
                if rid != rowKey { rows.append([]); rowKey = rid }
                rows[rows.count - 1].append(inlines)
                return
            }
            flushTable()
            let text = inlines.map(\.plain).joined()
            switch kinds.first {
            case .header(let level)?: out.append(.heading(level, inlines))
            case .codeBlock?: out.append(.code(text.hasSuffix("\n") ? String(text.dropLast()) : text))
            case .thematicBreak?: out.append(.rule)
            case .paragraph?:
                let items = kinds.filter { if case .listItem = $0 { true } else { false } }
                if case .listItem(let ordinal)? = items.first {
                    let ordered = kinds.drop { if case .listItem = $0 { false } else { true } }.dropFirst().first
                    let marker = ordered == .orderedList ? "\(ordinal)." : "\u{2022}"
                    out.append(.listItem(depth: items.count, marker: marker, inlines))
                } else if kinds.contains(.blockQuote) {
                    out.append(.quote(inlines))
                } else {
                    out.append(.paragraph(inlines))
                }
            default:
                // Block HTML and anything else: its literal text.
                let t = text.trimmingCharacters(in: .newlines)
                if !t.isEmpty { out.append(.paragraph([.text(t, [])])) }
            }
        }

        for run in doc.runs {
            let comps = run.presentationIntent?.components ?? []
            let k = comps.map(\.identity)
            if k != key {
                flush()
                key = k
                leaf = comps
            }
            inlines.append(inline(run, String(doc[run.range].characters)))
        }
        flush()
        flushTable()
        return out
    }
}
