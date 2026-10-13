// SPDX-License-Identifier: Apache-2.0

/// The default-deny filter in front of a tab view's terminal (ADR 0020,
/// decision 9; R-0133 item 2). Every byte from the workspace is untrusted:
/// only what's on these lists reaches SwiftTerm, so a program can't set the
/// Mac's clipboard, a window title, open links, move or report windows, draw
/// images, or change modes the app doesn't know.
///
/// - Text passes as UTF-8. A byte that can't be part of UTF-8 where it is (a
///   stray continuation byte, which an 8-bit terminal would read as a C1
///   control, or an invalid lead) becomes U+FFFD, and so does a C1 control
///   written as UTF-8 (U+0080–U+009F).
/// - C0: BS, HT, LF, VT, FF, CR, BEL (the view doesn't sound it), SO and SI
///   pass; the rest, and DEL, are dropped.
/// - CSI passes for an allowlist of final bytes (cursor, erase, insert and
///   delete, scroll, SGR, scroll region, tab stops, modes, and the queries,
///   whose replies the view drops). XTWINOPS (`CSI … t`) and the kitty
///   keyboard protocol are dropped. DECSET/DECRST keep only allowlisted
///   private modes.
/// - ESC sequences: DECSC/DECRC, IND/NEL/RI, charset designation, keypad
///   modes and RIS. RIS becomes a soft reset, home and erase screen: the
///   scrollback and its marks stay.
/// - OSC: 133 (the marks) and 8 (links, opened only as TerminalLinks says)
///   pass; everything else (titles, 7, 52, 1337, colours) is dropped.
/// - DCS, APC, PM and SOS strings are dropped whole.
/// - One sequence is at most `maxSequence` bytes (an OSC 8 at most
///   `maxLink`); a longer one is dropped whole, without being kept.
public struct TerminalFilter: Sendable {
    public static let maxSequence = 4096
    public static let maxLink = 2048

    /// The DEC private modes a program may set or reset.
    public static let privateModes: Set<Int> = [1, 6, 7, 12, 25, 47, 1000, 1002, 1003, 1004, 1006, 1047, 1048, 1049, 2004, 2026]
    /// The ANSI modes (SM/RM without `?`): insert and newline.
    public static let ansiModes: Set<Int> = [4, 20]

    private enum State: Sendable {
        case ground
        case esc
        case escIntermediate(UInt8)
        case csi
        case osc
        case oscEsc
        /// A DCS, APC, PM or SOS string, or an OSC too long to keep (which
        /// BEL also ends): dropped until its terminator.
        case dropString(endsOnBEL: Bool), dropStringEsc(endsOnBEL: Bool)
        case dropSequence // a CSI too long to keep: drop until its final byte
    }

    private var state = State.ground
    private var seq: [UInt8] = []
    private var utf8Left = 0
    /// A C2 lead byte held back: with 80–9F after it, it encodes a C1
    /// control, which becomes U+FFFD too.
    private var heldC2 = false

    public init() {}

    /// Filters a chunk; partial sequences are held for the next chunk.
    public mutating func filter(_ input: [UInt8]) -> [UInt8] {
        var out: [UInt8] = []
        out.reserveCapacity(input.count)
        for b in input { step(b, &out) }
        return out
    }

    /// How many bytes of a partial sequence are held now.
    public var held: Int { seq.count }

    private static let replacement: [UInt8] = [0xEF, 0xBF, 0xBD]

    private mutating func step(_ b: UInt8, _ out: inout [UInt8]) {
        // CAN and SUB end any sequence; ESC starts a new one (an OSC or
        // string may end with ESC \, handled in its own state).
        switch state {
        case .ground:
            ground(b, &out)
        case .esc:
            esc(b, &out)
        case let .escIntermediate(i):
            state = .ground
            if b >= 0x30 && b <= 0x7E && i != 0x23 {
                out += [0x1B, i, b] // charset designation: ESC ( B and the like
            } else if b == 0x1B {
                state = .esc
            }
        case .csi:
            if b == 0x1B { seq = []; state = .esc; return }
            if b == 0x18 || b == 0x1A { seq = []; state = .ground; return }
            if b < 0x20 { control(b, &out); return }
            if b >= 0x40 && b <= 0x7E {
                seq.append(b)
                out += csi(seq)
                seq = []
                state = .ground
            } else if b >= 0x20 && b <= 0x3F {
                seq.append(b)
                if seq.count > Self.maxSequence { seq = []; state = .dropSequence }
            } else {
                seq = []
                state = .ground
            }
        case .dropSequence:
            if b == 0x1B { state = .esc } else if (b >= 0x40 && b <= 0x7E) || b == 0x18 || b == 0x1A { state = .ground }
        case .osc:
            if b == 0x07 { out += osc(seq); seq = []; state = .ground; return }
            if b == 0x1B { state = .oscEsc; return }
            if b == 0x18 || b == 0x1A { seq = []; state = .ground; return }
            seq.append(b)
            if seq.count > Self.maxSequence { seq = []; state = .dropString(endsOnBEL: true) }
        case .oscEsc:
            if b == 0x5C { out += osc(seq) }
            seq = []
            state = .ground
            if b != 0x5C { esc(b, &out) }
        case let .dropString(bel):
            if b == 0x1B { state = .dropStringEsc(endsOnBEL: bel) } else if b == 0x18 || b == 0x1A || (bel && b == 0x07) { state = .ground }
        case let .dropStringEsc(bel):
            state = b == 0x5C ? .ground : b == 0x1B ? .dropStringEsc(endsOnBEL: bel) : .dropString(endsOnBEL: bel)
        }
    }

    private mutating func ground(_ b: UInt8, _ out: inout [UInt8]) {
        if heldC2 {
            heldC2 = false
            switch b {
            case 0x80 ... 0x9F: out += Self.replacement; return
            case 0xA0 ... 0xBF: out += [0xC2, b]; return
            default: out.append(0xC2) // an unfinished character; SwiftTerm draws U+FFFD
            }
        }
        if utf8Left > 0 {
            if b & 0xC0 == 0x80 {
                utf8Left -= 1
                out.append(b)
                return
            }
            utf8Left = 0 // an unfinished character; SwiftTerm draws U+FFFD
        }
        switch b {
        case 0x1B:
            state = .esc
        case 0x00 ..< 0x20, 0x7F:
            control(b, &out)
        case 0x20 ..< 0x7F:
            out.append(b)
        case 0xC2:
            heldC2 = true
        case 0xC3 ... 0xDF:
            utf8Left = 1; out.append(b)
        case 0xE0 ... 0xEF:
            utf8Left = 2; out.append(b)
        case 0xF0 ... 0xF4:
            utf8Left = 3; out.append(b)
        default:
            out += Self.replacement // a stray continuation byte (C1) or an invalid lead
        }
    }

    private func control(_ b: UInt8, _ out: inout [UInt8]) {
        switch b {
        case 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F: out.append(b)
        default: break
        }
    }

    private mutating func esc(_ b: UInt8, _ out: inout [UInt8]) {
        state = .ground
        switch b {
        case 0x5B: state = .csi; seq = [] // [
        case 0x5D: state = .osc; seq = [] // ]
        case 0x50, 0x5F, 0x5E, 0x58: state = .dropString(endsOnBEL: false) // P _ ^ X
        case 0x37, 0x38, 0x44, 0x45, 0x4D, 0x3D, 0x3E: out += [0x1B, b] // 7 8 D E M = >
        case 0x63: out += Array("\u{1b}[!p\u{1b}[H\u{1b}[2J".utf8) // RIS: keep scrollback and marks
        case 0x28, 0x29, 0x2A, 0x2B, 0x23: state = .escIntermediate(b) // ( ) * + #
        case 0x1B: state = .esc
        case 0x18, 0x1A: break
        default:
            if b < 0x20 { control(b, &out) }
        }
    }

    /// A whole CSI sequence (parameters, intermediates and final byte).
    private func csi(_ s: [UInt8]) -> [UInt8] {
        guard let final = s.last else { return [] }
        let body = s.dropLast()
        let prefix: UInt8? = body.first.flatMap { (0x3C ... 0x3F).contains($0) ? $0 : nil }
        let rest = prefix == nil ? body[...] : body.dropFirst()
        let params = rest.prefix { $0 >= 0x30 && $0 <= 0x3B }
        let inter = rest.dropFirst(params.count)
        if inter.contains(where: { $0 < 0x20 || $0 > 0x2F }) { return [] }
        let whole = [0x1B, 0x5B] + s
        let interString = String(decoding: inter, as: UTF8.self)
        switch (prefix, interString, final) {
        case (nil, "", _) where Array("@ABCDEFGHIJKLMPSTXZ`abdefgmnrsu".utf8).contains(final):
            if final == 0x54, params.split(separator: 0x3B).count > 1 { return [] } // CSI T with 5 params: mouse tracking
            return whole
        case (nil, "", 0x63), (0x3E, "", 0x63), (0x3D, "", 0x63): return whole // DA (the view drops the reply)
        case (0x3F, "", 0x6E): return whole // DECXCPR, DSR
        case (0x3E, "", 0x71): return whole // XTVERSION
        case (nil, " ", 0x71): return whole // DECSCUSR, cursor style
        case (nil, "!", 0x70): return whole // DECSTR, soft reset
        case (nil, "$", 0x70), (0x3F, "$", 0x70): return whole // DECRQM
        case (0x3F, "", 0x68), (0x3F, "", 0x6C):
            return modes(params, keep: Self.privateModes, prefix: [0x3F], final: final)
        case (nil, "", 0x68), (nil, "", 0x6C):
            return modes(params, keep: Self.ansiModes, prefix: [], final: final)
        default:
            return [] // XTWINOPS (t), kitty keys (> u, = u, ? u, < u), and anything else
        }
    }

    private func modes(_ params: ArraySlice<UInt8>, keep: Set<Int>, prefix: [UInt8], final: UInt8) -> [UInt8] {
        let kept = params.split(separator: 0x3B, omittingEmptySubsequences: true)
            .compactMap { Int(String(decoding: $0, as: UTF8.self)) }
            .filter { keep.contains($0) }
        guard !kept.isEmpty else { return [] }
        return [0x1B, 0x5B] + prefix + Array(kept.map(String.init).joined(separator: ";").utf8) + [final]
    }

    /// A whole OSC payload, without its terminator.
    private func osc(_ s: [UInt8]) -> [UInt8] {
        let number = String(decoding: s.prefix { $0 != 0x3B }, as: UTF8.self)
        guard number == "133" || (number == "8" && s.count <= Self.maxLink) else { return [] }
        // Its payload is text: no controls inside.
        guard !s.contains(where: { $0 < 0x20 || $0 == 0x7F }) else { return [] }
        return [0x1B, 0x5D] + s + [0x1B, 0x5C]
    }
}

/// Pasted text, cleaned before it's sent (ADR 0020, R-0133 item 3): no
/// bracketed-paste end marker (which would end the paste early and run the
/// rest as typed commands) and no control characters but tab, CR and LF, so
/// nothing in the clipboard can act as a key.
public enum PasteText {
    public static func clean(_ text: String) -> String {
        let unmarked = text.replacingOccurrences(of: "\u{1b}[201~", with: "").replacingOccurrences(of: "\u{1b}[200~", with: "")
        return String(String.UnicodeScalarView(unmarked.unicodeScalars.filter { c in
            switch c.value {
            case 0x09, 0x0A, 0x0D: true
            case 0x00 ... 0x1F, 0x7F ... 0x9F: false
            default: true
            }
        }))
    }
}
