// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation
@testable import PortenvKit
import Testing

/// ADR 0019's recovery key, against core/keys/recovery/testdata/vectors.json,
/// the file the Go tests read too.
struct RecoveryKeyTests {
    struct Vectors: Decodable {
        struct RFC5869: Decodable {
            let ikm_hex, salt_hex, info_hex, okm_hex: String
            let length: Int
        }
        struct Generate: Decodable { let bytes_hex, grouped: String }
        struct Parse: Decodable {
            let input: String
            let canonical, error, character, message: String?
            let length: Int?
        }
        struct Derive: Decodable { let recovery, box_id, derived: String }

        let alphabet, salt, info_prefix, box_id_pattern: String
        let rfc5869_case1: RFC5869
        let generate: [Generate]
        let parse: [Parse]
        let derive: [Derive]
        let invalid_box_ids: [String]
    }

    static let vectors: Vectors = {
        // …/apps/mac/Tests/PortenvKitTests/this file → the repository root.
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let url = root.appendingPathComponent("core/keys/recovery/testdata/vectors.json")
        return try! JSONDecoder().decode(Vectors.self, from: Data(contentsOf: url))
    }()

    static let adrVector = "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5"

    static func hex(_ s: String) -> [UInt8] {
        var out: [UInt8] = []
        var i = s.startIndex
        while i < s.endIndex {
            let j = s.index(i, offsetBy: 2)
            out.append(UInt8(s[i..<j], radix: 16)!)
            i = j
        }
        return out
    }

    static func hexString(_ b: some Sequence<UInt8>) -> String {
        b.map { String(format: "%02x", $0) }.joined()
    }

    static func randomKeys(_ n: Int) throws -> [RecoveryKey] {
        try (0..<n).map { _ in try RecoveryKey.generate() }
    }

    static func isTypo(_ s: String) -> Bool {
        if case .failure(let e) = Result(catching: { try RecoveryKey(parsing: s) }),
           let pe = e as? RecoveryKey.ParseError, pe == .typo { return true }
        return false
    }

    // 1. Format and alphabet.

    @Test func alphabet() {
        #expect(RecoveryKey.alphabet == Self.vectors.alphabet)
        #expect(Set(RecoveryKey.alphabet).count == 32)
        #expect(RecoveryKey.alphabet.count == 32)
        for c in "01IO" { #expect(!RecoveryKey.alphabet.contains(c)) }
    }

    @Test func generatedKeysAreGroupedAndPassTheirCheck() throws {
        for k in try Self.randomKeys(1000) {
            let g = k.grouped
            #expect(g.count == 29)
            for (i, c) in g.enumerated() { #expect((i % 5 == 4) == (c == "-")) }
            #expect(try RecoveryKey(parsing: g).canonical == k.canonical)
        }
    }

    @Test func formattingAndReadingRoundTrip() throws {
        let k = try RecoveryKey(parsing: Self.adrVector)
        #expect(k.grouped == Self.adrVector)
        #expect(k.canonical == Self.adrVector.replacingOccurrences(of: "-", with: ""))
        for s in [Self.adrVector.lowercased(), k.canonical,
                  Self.adrVector.lowercased().replacingOccurrences(of: "-", with: " "),
                  " " + Self.adrVector.replacingOccurrences(of: "-", with: "  - ") + " "] {
            #expect(try RecoveryKey(parsing: s).grouped == Self.adrVector)
        }
    }

    /// Generation uses each byte's low 5 bits: a fake byte source gives a
    /// known key.
    @Test func generateFromKnownBytes() throws {
        for g in Self.vectors.generate {
            var asked: [Int] = []
            let k = try RecoveryKey.generate { n in asked.append(n); return Self.hex(g.bytes_hex) }
            #expect(k.grouped == g.grouped)
            #expect(asked == [23])
        }
    }

    /// A failing byte source is an error, never a weaker key.
    @Test func generateFailsWithoutRandomBytes() {
        struct NoBytes: Error {}
        #expect(throws: NoBytes.self) { try RecoveryKey.generate { _ in throw NoBytes() } }
        #expect(throws: RecoveryKey.GenerateError.self) { try RecoveryKey.generate { _ in [UInt8](repeating: 0, count: 22) } }
    }

    @Test func parseVectors() {
        for p in Self.vectors.parse {
            let result = Result { try RecoveryKey(parsing: p.input) }
            switch (result, p.error) {
            case (.success(let k), nil):
                #expect(k.canonical == p.canonical, "\(p.input)")
            case (.failure(let e as RecoveryKey.ParseError), let kind?):
                #expect(e.message == p.message, "\(p.input)")
                switch kind {
                case "character": #expect(e == .unknownCharacter(p.character!), "\(p.input)")
                case "length": #expect(e == .wrongLength(p.length!), "\(p.input)")
                case "typo": #expect(e == .typo, "\(p.input)")
                default: Issue.record("unknown error kind \(kind) in vectors")
                }
            default:
                Issue.record("Parse(\(p.input.debugDescription)) = \(result), want \(p.error ?? "success")")
            }
        }
    }

    // 2. The check character.

    static let alphabetChars = Array(RecoveryKey.alphabet)

    /// Every single wrong character, the check character included, is caught.
    @Test func checkCatchesEverySingleChange() throws {
        for k in try [RecoveryKey(parsing: Self.adrVector)] + Self.randomKeys(1000) {
            var c = Array(k.canonical)
            for i in c.indices {
                let orig = c[i]
                for a in Self.alphabetChars where a != orig {
                    c[i] = a
                    if !Self.isTypo(String(c)) {
                        Issue.record("\(k.grouped): missed \(a) at \(i)")
                        return
                    }
                }
                c[i] = orig
            }
        }
    }

    /// Every swap of two different characters, adjacent or not, is caught.
    @Test func checkCatchesEverySwap() throws {
        for k in try [RecoveryKey(parsing: Self.adrVector)] + Self.randomKeys(1000) {
            var c = Array(k.canonical)
            for i in c.indices {
                for j in (i + 1)..<c.count where c[i] != c[j] {
                    c.swapAt(i, j)
                    if !Self.isTypo(String(c)) {
                        Issue.record("\(k.grouped): missed the swap of \(i) and \(j)")
                        return
                    }
                    c.swapAt(i, j)
                }
            }
        }
    }

    /// Two wrong characters slip through at about 1 in 31 (ADR 0019),
    /// measured over a sample of the vector key's pairs of changes.
    @Test func twoCharacterErrorRate() throws {
        var c = Array(try RecoveryKey(parsing: Self.adrVector).canonical)
        var tried = 0, missed = 0
        for i in c.indices {
            for j in (i + 1)..<c.count {
                let (oi, oj) = (c[i], c[j])
                for a in Self.alphabetChars where a != oi {
                    for b in Self.alphabetChars where b != oj {
                        c[i] = a; c[j] = b
                        tried += 1
                        if (try? RecoveryKey(parsing: String(c))) != nil { missed += 1 }
                    }
                }
                c[i] = oi; c[j] = oj
            }
        }
        let rate = Double(missed) / Double(tried)
        print("two-character errors: \(missed) of \(tried) missed (1 in \(String(format: "%.1f", 1 / rate)))")
        #expect(rate > 1.0 / 33 && rate < 1.0 / 29)
    }

    // 3. The derivation.

    @Test func deriveVectors() throws {
        #expect(RecoveryKey.salt == Self.vectors.salt)
        #expect(RecoveryKey.infoPrefix == Self.vectors.info_prefix)
        for d in Self.vectors.derive {
            #expect(try RecoveryKey(parsing: d.recovery).password(boxID: d.box_id) == d.derived, "\(d.recovery) \(d.box_id)")
        }
    }

    /// The ADR's own two vectors are in the file.
    @Test func adrVectorsAreInTheFile() {
        let want = [
            "box-0123456789ab": "b4eef5e9da4b5563a65819e2bd99ec3291f9da036e6688e6d6be20cd120d66af",
            "box-0123456789ac": "32e479fe2dcc4a2fa0b28f9c62f85f88a130de773bf72434019522fa769465e9",
        ]
        for (box, pw) in want {
            #expect(Self.vectors.derive.contains { $0.recovery == Self.adrVector && $0.box_id == box && $0.derived == pw })
        }
    }

    @Test func hkdfMatchesRFC5869Case1() {
        let v = Self.vectors.rfc5869_case1
        let okm = RecoveryKey.hkdfSHA256(ikm: Self.hex(v.ikm_hex), salt: Self.hex(v.salt_hex),
                                         info: Self.hex(v.info_hex), length: v.length)
        #expect(Self.hexString(okm) == v.okm_hex)
    }

    /// Changing the salt, the info's separator or the order of the fields
    /// fails the vectors.
    @Test func derivationMutationsFailTheVectors() {
        let d = Self.vectors.derive[0]
        let ikm = Array(d.recovery.replacingOccurrences(of: "-", with: "").utf8)
        let box = Array(d.box_id.utf8), salt = Array(RecoveryKey.salt.utf8), prefix = Array(RecoveryKey.infoPrefix.utf8)
        func h(_ ikm: [UInt8], _ salt: [UInt8], _ info: [UInt8]) -> String {
            Self.hexString(RecoveryKey.hkdfSHA256(ikm: ikm, salt: salt, info: info, length: 32))
        }
        #expect(h(ikm, salt, prefix + [0] + box) == d.derived)
        let mutants: [String: String] = [
            "salt": h(ikm, Array("portenv recovery key v2".utf8), prefix + [0] + box),
            "no salt": h(ikm, [], prefix + [0] + box),
            "no separator": h(ikm, salt, prefix + box),
            "space separator": h(ikm, salt, prefix + [0x20] + box),
            "box ID first": h(ikm, salt, box + [0] + prefix),
            "salt and info": h(ikm, prefix + [0] + box, salt),
            "hyphens in IKM": h(Array(d.recovery.utf8), salt, prefix + [0] + box),
            "no check in IKM": h(Array(ikm.prefix(23)), salt, prefix + [0] + box),
            "lower-case IKM": h(Array(String(decoding: ikm, as: UTF8.self).lowercased().utf8), salt, prefix + [0] + box),
            "plain SHA-256": Self.hexString(SHA256.hash(data: ikm + box)),
        ]
        for (name, out) in mutants { #expect(out != d.derived, "mutation \(name) still matches the vector") }
    }

    @Test func derivationProperties() throws {
        let k = try RecoveryKey(parsing: Self.adrVector)
        let a = try k.password(boxID: "box-0123456789ab")
        #expect(try k.password(boxID: "box-0123456789ab") == a)
        #expect(a.count == 64 && a == a.lowercased() && a.allSatisfy(\.isHexDigit))
        // Box IDs that differ by one character.
        var seen: Set<String> = [a]
        var base = Array("box-0123456789ab")
        for i in 4..<base.count {
            let orig = base[i]
            for c in "0123456789abcdef" where c != orig {
                base[i] = c
                #expect(seen.insert(try k.password(boxID: String(base))).inserted, "\(String(base)) repeats a password")
            }
            base[i] = orig
        }
        // Different keys.
        for other in try Self.randomKeys(100) {
            #expect(try other.password(boxID: "box-0123456789ab") != a)
        }
    }

    @Test func invalidBoxIDsAreRefused() throws {
        let k = try RecoveryKey(parsing: Self.adrVector)
        for id in Self.vectors.invalid_box_ids {
            #expect(!RecoveryKey.isValidBoxID(id), "\(id.debugDescription)")
            #expect(throws: RecoveryKey.DeriveError.self) { try k.password(boxID: id) }
        }
        for d in Self.vectors.derive { #expect(RecoveryKey.isValidBoxID(d.box_id)) }
    }

    /// A key never prints itself, so a log line or an interpolation can't
    /// carry it by accident.
    @Test func keyNeverPrintsItself() throws {
        let k = try RecoveryKey(parsing: Self.adrVector)
        for out in ["\(k)", String(describing: k), String(reflecting: k), "\([k])", "\(Optional(k) as Any)"] {
            #expect(!out.uppercased().contains("K7QF") && !out.uppercased().contains("3NZ5"), "\(out)")
        }
        var dumped = ""
        dump(k, to: &dumped)
        #expect(!dumped.uppercased().contains("K7QF"), "\(dumped)")
    }

    @Test func wipe() throws {
        var k = try RecoveryKey(parsing: Self.adrVector)
        k.wipe()
        #expect(k.canonical.isEmpty)
        #expect(throws: RecoveryKey.DeriveError.self) { try k.password(boxID: "box-0123456789ab") }
    }
}
