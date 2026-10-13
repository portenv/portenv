// SPDX-License-Identifier: Apache-2.0

import Foundation
@testable import PortenvKit
import Testing

/// The default-deny filter in front of a tab view (ADR 0020, decision 9;
/// R-0133 item 2): each rule, its limits, and a fuzz run.
struct TerminalFilterTests {
    func run(_ s: String) -> String {
        var f = TerminalFilter()
        return String(decoding: f.filter(Array(s.utf8)), as: UTF8.self)
    }

    func run(_ b: [UInt8]) -> [UInt8] {
        var f = TerminalFilter()
        return f.filter(b)
    }

    static let esc = "\u{1b}"

    @Test func whatPasses() {
        let pass = [
            "plain text é 日本 🙂", "\r\n\t\u{08}\u{07}\u{0b}\u{0c}\u{0e}\u{0f}",
            "\(Self.esc)[1;31mred\(Self.esc)[0m", "\(Self.esc)[38;2;10;20;30m", "\(Self.esc)[4:3m",
            "\(Self.esc)[H", "\(Self.esc)[10;5H", "\(Self.esc)[2J", "\(Self.esc)[K", "\(Self.esc)[3L", "\(Self.esc)[2M",
            "\(Self.esc)[5@", "\(Self.esc)[4P", "\(Self.esc)[2S", "\(Self.esc)[2T", "\(Self.esc)[7X", "\(Self.esc)[3;20r",
            "\(Self.esc)[g", "\(Self.esc)[6n", "\(Self.esc)[c", "\(Self.esc)[>c", "\(Self.esc)[?6n", "\(Self.esc)[>q",
            "\(Self.esc)[2 q", "\(Self.esc)[!p", "\(Self.esc)[?2004$p", "\(Self.esc)[s", "\(Self.esc)[u",
            "\(Self.esc)[?1049h", "\(Self.esc)[?25l", "\(Self.esc)[?1;6;7h", "\(Self.esc)[?1000;1006h", "\(Self.esc)[?2004h",
            "\(Self.esc)[?2026l", "\(Self.esc)[4h", "\(Self.esc)[20l",
            "\(Self.esc)7", "\(Self.esc)8", "\(Self.esc)D", "\(Self.esc)E", "\(Self.esc)M", "\(Self.esc)=", "\(Self.esc)>",
            "\(Self.esc)(B", "\(Self.esc))0",
        ]
        for p in pass { #expect(run(p) == p, "\(p.debugDescription) -> \(run(p).debugDescription)") }
        // OSC 133 and 8 pass, terminated with ST.
        #expect(run("\(Self.esc)]133;D;0\u{07}") == "\(Self.esc)]133;D;0\(Self.esc)\\")
        #expect(run("\(Self.esc)]8;;https://example.com/\(Self.esc)\\x\(Self.esc)]8;;\u{07}")
            == "\(Self.esc)]8;;https://example.com/\(Self.esc)\\x\(Self.esc)]8;;\(Self.esc)\\")
    }

    @Test func whatIsDropped() {
        let dropped = [
            "\(Self.esc)]0;title\u{07}", "\(Self.esc)]2;title\(Self.esc)\\", "\(Self.esc)]1;icon\u{07}", "\(Self.esc)]7;file:///x\u{07}",
            "\(Self.esc)]52;c;aGk=\u{07}", "\(Self.esc)]52;c;?\u{07}", "\(Self.esc)]1337;File=name=eA==:aGk=\u{07}",
            "\(Self.esc)]1337;SetUserVar=a=Yg==\u{07}", "\(Self.esc)]4;1;?\u{07}", "\(Self.esc)]10;?\u{07}", "\(Self.esc)]11;rgb:0/0/0\u{07}",
            "\(Self.esc)]777;notify;a;b\u{07}", "\(Self.esc)]9;hi\u{07}",
            "\(Self.esc)P$qm\(Self.esc)\\", "\(Self.esc)P+q544e\(Self.esc)\\", "\(Self.esc)Pq#0;2;0;0;0#0~~\(Self.esc)\\",
            "\(Self.esc)_Gf=24,s=1,v=1;AAAA\(Self.esc)\\", "\(Self.esc)^pm\(Self.esc)\\", "\(Self.esc)Xsos\(Self.esc)\\",
            "\(Self.esc)[8;24;80t", "\(Self.esc)[14t", "\(Self.esc)[21t", "\(Self.esc)[3;1;1t", "\(Self.esc)[>1u", "\(Self.esc)[=1;1u",
            "\(Self.esc)[?u", "\(Self.esc)[<u", "\(Self.esc)[?1005h", "\(Self.esc)[?1015h", "\(Self.esc)[?5h", "\(Self.esc)[?3h",
            "\(Self.esc)[12h", "\(Self.esc)[?2J", "\(Self.esc)[1;2;3;4;5T", "\(Self.esc)#8", "\u{00}\u{05}\u{1a}\u{7f}\u{01}",
            "\(Self.esc)\\", "\(Self.esc)[1;2\u{18}",
        ]
        for d in dropped { #expect(run(d).isEmpty, "\(d.debugDescription) -> \(run(d).debugDescription)") }
        // Text around a dropped sequence stays; a mixed mode list keeps only allowed modes.
        #expect(run("a\(Self.esc)]0;t\u{07}b\(Self.esc)[?1;5;2004hc") == "ab\(Self.esc)[?1;2004hc")
    }

    /// An 8-bit C1 control (a stray continuation byte) or an invalid lead
    /// can't act as a control: it becomes U+FFFD.
    @Test func eightBitControlsBecomeReplacementCharacters() {
        for b: UInt8 in [0x9B, 0x9D, 0x90, 0x9F, 0x80, 0xC0, 0xC1, 0xF5, 0xFF] {
            #expect(run([0x61, b, 0x62]) == [0x61, 0xEF, 0xBF, 0xBD, 0x62], "0x\(String(b, radix: 16))")
        }
        #expect(run(Array("é".utf8)) == Array("é".utf8))
        // A C1 control written as UTF-8 (U+009B, CSI) is replaced; U+00A0 isn't.
        #expect(run([0xC2, 0x9B, 0x31, 0x6D]) == [0xEF, 0xBF, 0xBD, 0x31, 0x6D])
        #expect(run([0xC2, 0xA0]) == [0xC2, 0xA0])
        var f = TerminalFilter()
        #expect(f.filter([0x61, 0xC2]) + f.filter([0x9D]) == [0x61, 0xEF, 0xBF, 0xBD])
    }

    /// RIS clears the screen and resets modes but keeps the scrollback and
    /// its marks.
    @Test func risKeepsTheScrollback() {
        #expect(run("\(Self.esc)c") == "\(Self.esc)[!p\(Self.esc)[H\(Self.esc)[2J")
    }

    @Test func limits() {
        var f = TerminalFilter()
        let longOSC = "\(Self.esc)]133;" + String(repeating: "x", count: TerminalFilter.maxSequence + 10) + "\u{07}after"
        #expect(String(decoding: f.filter(Array(longOSC.utf8)), as: UTF8.self) == "after")
        let longLink = "\(Self.esc)]8;;https://e.com/" + String(repeating: "a", count: TerminalFilter.maxLink) + "\u{07}x"
        #expect(run(longLink) == "x")
        let longCSI = "\(Self.esc)[" + String(repeating: "1;", count: TerminalFilter.maxSequence) + "mok"
        #expect(run(longCSI) == "ok")
        // Memory: a sequence that never ends holds at most maxSequence bytes.
        var g = TerminalFilter()
        _ = g.filter(Array(("\(Self.esc)]133;" + String(repeating: "y", count: 100_000)).utf8))
        #expect(g.held <= TerminalFilter.maxSequence)
        _ = g.filter(Array(("\(Self.esc)P" + String(repeating: "z", count: 100_000)).utf8))
        #expect(g.held <= TerminalFilter.maxSequence)
    }

    /// Chunking doesn't change anything: a stream split anywhere filters to
    /// the same bytes.
    @Test func splitsDontMatter() {
        let s = Array("a\(Self.esc)[1;31mé\(Self.esc)]133;A\u{07}\(Self.esc)]0;x\u{07}日\(Self.esc)P$q\(Self.esc)\\z".utf8)
        let whole = run(s)
        for cut in 1 ..< s.count {
            var f = TerminalFilter()
            #expect(f.filter(Array(s[..<cut])) + f.filter(Array(s[cut...])) == whole, "cut at \(cut)")
        }
    }

    // MARK: Fuzzing

    struct Rng: RandomNumberGenerator {
        var state: UInt64
        mutating func next() -> UInt64 {
            state ^= state << 13; state ^= state >> 7; state ^= state << 17
            return state
        }
    }

    static func fixtureStreams() -> [[UInt8]] {
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("../../../../tests/spike/q3/fixtures").standardized
        let files = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        return files.filter { $0.pathExtension == "jsonl" }.compactMap { url in
            guard let text = try? String(contentsOf: url, encoding: .utf8) else { return nil }
            var out: [UInt8] = []
            for line in text.split(separator: "\n") {
                guard let o = try? JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any],
                      o["path"] as? String == "C", let b = o["b64"] as? String, let d = Data(base64Encoded: b) else { continue }
                out += d
            }
            return out
        }
    }

    /// What the output may never hold, whatever came in: no OSC but 133 and
    /// 8, no DCS, APC, PM or SOS, no XTWINOPS, no C1 byte outside UTF-8,
    /// no NUL or DEL; and filtering it again changes nothing.
    func checkInvariants(_ input: [UInt8], seed: UInt64) {
        var f = TerminalFilter()
        let out = f.filter(input)
        #expect(f.held <= TerminalFilter.maxSequence + 1)
        // Filtering again changes nothing (a space ends any character the
        // first pass left unfinished at the end).
        var again = TerminalFilter()
        #expect(again.filter(out + [0x20]) == out + [0x20], "not idempotent (seed \(seed))")
        let s = String(decoding: out, as: UTF8.self)
        let osc = try! Regex("\u{1b}\\]([0-9]*)")
        for m in s.matches(of: osc) {
            let n = String(s[m.range].dropFirst(2))
            #expect(n == "133" || n == "8", "OSC \(n) got through (seed \(seed))")
        }
        for bad in ["\u{1b}P", "\u{1b}_", "\u{1b}^", "\u{1b}X"] { #expect(!s.contains(bad), "\(bad.debugDescription) got through (seed \(seed))") }
        #expect(!out.contains(0x00) && !out.contains(0x7F) && !out.contains(0x05), "a dropped control got through (seed \(seed))")
        for i in out.indices.dropLast() where out[i] == 0xC2 {
            #expect(!(0x80 ... 0x9F).contains(out[i + 1]), "a C1 control in UTF-8 got through (seed \(seed))")
        }
        let t = try! Regex("\u{1b}\\[[0-9;]*t")
        #expect(s.firstMatch(of: t) == nil, "XTWINOPS got through (seed \(seed))")
    }

    @Test func fuzzRandomBytes() {
        for seed: UInt64 in 1 ... 120 {
            var rng = Rng(state: seed &* 0x9E37_79B9_7F4A_7C15 | 1)
            let alphabet: [UInt8] = Array(0 ... 255) + Array(repeating: 0x1B, count: 40) + Array("[]P_^X\\;?0123456789mtqh\u{07}".utf8)
            let input = (0 ..< Int.random(in: 1 ... 2000, using: &rng)).map { _ in alphabet.randomElement(using: &rng)! }
            checkInvariants(input, seed: seed)
        }
    }

    @Test func fuzzMutatedFixtures() {
        let streams = Self.fixtureStreams()
        #expect(!streams.isEmpty, "no spike fixtures")
        let inserts: [[UInt8]] = ["\u{1b}]52;c;aGk=\u{07}", "\u{1b}]0;t", "\u{1b}P", "\u{1b}[8;1;1t", "\u{1b}c", "\u{1b}]8;;file:///x\u{07}"].map { Array($0.utf8) } + [[0x9D], [0xC0]]
        for (i, stream) in streams.enumerated() {
            checkInvariants(stream, seed: UInt64(i))
            for seed: UInt64 in 1 ... 40 {
                var rng = Rng(state: (seed << 8 | UInt64(i)) | 1)
                var s = stream
                for _ in 0 ..< 20 where !s.isEmpty {
                    let at = Int.random(in: 0 ..< s.count, using: &rng)
                    switch Int.random(in: 0 ..< 3, using: &rng) {
                    case 0: s.insert(contentsOf: inserts.randomElement(using: &rng)!, at: at)
                    case 1: s[at] = UInt8.random(in: 0 ... 255, using: &rng)
                    default: s.remove(at: at)
                    }
                }
                checkInvariants(s, seed: seed)
            }
        }
    }
}

struct PasteTextTests {
    @Test func pasteCantEndItselfOrActAsKeys() {
        let evil = "echo hi\u{1b}[201~rm -rf ~\r\u{00}\u{1b}\u{03}\u{7f}\u{9b}x\ttab\nnext"
        let clean = PasteText.clean(evil)
        #expect(clean == "echo hirm -rf ~\rx\ttab\nnext")
        #expect(!clean.contains("\u{1b}"))
        #expect(PasteText.clean("plain é 日本") == "plain é 日本")
        #expect(PasteText.clean("\u{1b}[200~x\u{1b}[201~") == "x")
    }
}
