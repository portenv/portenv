// SPDX-License-Identifier: Apache-2.0

import CryptoKit
import Foundation
import Security

/// The recovery key (ADR 0019): 24 characters from a 32-character alphabet
/// without look-alikes, the last one a check character over GF(32), shown
/// as six groups of four. Each box's recovery password is derived from it
/// with HKDF-SHA256 and the box ID. The Go copy is core/keys/recovery; both
/// run against core/keys/recovery/testdata/vectors.json.
///
/// It never prints itself (description, debugDescription and its mirror are
/// a placeholder), so a log line or an interpolation can't carry it by
/// accident. `grouped` is for Print and Copy only.
public struct RecoveryKey: Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    /// The digits 2 to 9 and every letter but I and O: 5 bits each.
    public static let alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
    public static let length = 24
    static let random = length - 1
    static let alphabetBytes = Array(alphabet.utf8)

    /// The HKDF inputs. The info is `infoPrefix`, a zero byte, then the box ID.
    public static let salt = "portenv recovery key v1"
    public static let infoPrefix = "portenv restic password v1"

    /// Symbol values, 0 to 31; empty once wiped.
    private var symbols: [UInt8]

    private init(symbols: [UInt8]) { self.symbols = symbols }

    // MARK: Making one

    public struct GenerateError: Error, Equatable {
        public let message: String
    }

    /// The system CSPRNG. A failure is an error: first run stops, and never
    /// falls back to anything weaker.
    public static func systemRandomBytes(_ count: Int) throws -> [UInt8] {
        var bytes = [UInt8](repeating: 0, count: count)
        guard SecRandomCopyBytes(kSecRandomDefault, count, &bytes) == errSecSuccess else {
            throw GenerateError(message: "This Mac couldn't make random numbers for the recovery key.")
        }
        return bytes
    }

    /// A new key from 23 bytes of `randomBytes`, keeping each byte's low 5
    /// bits: uniform symbols, since 256 is a multiple of 32.
    public static func generate(randomBytes: (Int) throws -> [UInt8] = systemRandomBytes) throws -> RecoveryKey {
        var bytes = try randomBytes(random)
        defer { for i in bytes.indices { bytes[i] = 0 } }
        guard bytes.count == random else {
            throw GenerateError(message: "This Mac couldn't make random numbers for the recovery key.")
        }
        var values = bytes.map { $0 & 31 }
        values.append(syndrome(values))
        return RecoveryKey(symbols: values)
    }

    // MARK: Reading a typed key

    /// Why a typed key was refused. `message` is what the app shows; it
    /// never contains the key.
    public enum ParseError: Error, Equatable, LocalizedError {
        /// The first character outside the alphabet, as typed.
        case unknownCharacter(String)
        /// The first character outside the alphabet can't be shown: a
        /// control, format, private-use or unassigned one (category C).
        case hiddenCharacter
        /// How many letters and digits were read.
        case wrongLength(Int)
        case typo

        public var message: String {
            switch self {
            case .unknownCharacter(let c): "Recovery keys don't use the character \(c)."
            case .hiddenCharacter: "Recovery keys don't use one of the characters you typed."
            case .wrongLength(let n): "A recovery key has 24 letters and digits. This one has \(n)."
            case .typo: "This recovery key has a typo. Check each group against your copy."
            }
        }

        public var errorDescription: String? { message }
    }

    /// Case doesn't matter (a to z only), and separators (`isSeparator`) are
    /// ignored. Any other character is refused by name, the first one as
    /// typed, or without its name when it can't be shown; then the length is
    /// checked, then the check character.
    public init(parsing text: String) throws {
        var values: [UInt8] = []
        values.reserveCapacity(Self.length)
        for scalar in text.unicodeScalars {
            if Self.isSeparator(scalar) { continue }
            var v = scalar.value
            if v >= 0x61 && v <= 0x7A { v -= 0x20 } // a–z only, never Unicode case mapping
            guard v < 128, let i = Self.alphabetBytes.firstIndex(of: UInt8(v)) else {
                for i in values.indices { values[i] = 0 }
                throw Self.isHidden(scalar) ? ParseError.hiddenCharacter : ParseError.unknownCharacter(String(scalar))
            }
            values.append(UInt8(i))
        }
        guard values.count == Self.length else {
            let n = values.count
            for i in values.indices { values[i] = 0 }
            throw ParseError.wrongLength(n)
        }
        guard Self.syndrome(values) == 0 else {
            for i in values.indices { values[i] = 0 }
            throw ParseError.typo
        }
        symbols = values
    }

    /// Whether a typed key may carry this between its characters (R-0104):
    /// pasted keys bring newlines and no-break spaces, and notes apps, mail
    /// and autocorrect turn hyphens into dashes. The set is Unicode's
    /// White_Space characters and the dashes below, listed here rather than
    /// taken from `properties.isWhitespace` so that Swift and Go agree
    /// whatever Unicode version each has; the vectors file lists it too.
    public static func isSeparator(_ s: Unicode.Scalar) -> Bool {
        switch s.value {
        case 0x09...0x0D, 0x20, 0x85, 0xA0, 0x1680, 0x2000...0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
            true // White_Space
        case 0x2D, 0x2010...0x2015, 0x2212, 0xFE58, 0xFE63, 0xFF0D:
            true // hyphen, the Unicode hyphens and dashes, minus, and their small and full-width forms
        default:
            false
        }
    }

    /// Unicode's category C (control, format, surrogate, private use or
    /// unassigned): a character a message can't show.
    static func isHidden(_ s: Unicode.Scalar) -> Bool {
        switch s.properties.generalCategory {
        case .control, .format, .surrogate, .privateUse, .unassigned: true
        default: false
        }
    }

    // MARK: Text

    /// The 24 characters, upper case, without hyphens; empty once wiped.
    public var canonical: String {
        String(decoding: symbols.map { Self.alphabetBytes[Int($0)] }, as: UTF8.self)
    }

    /// Six groups of four joined by hyphens, for Print and Copy.
    public var grouped: String {
        let c = Array(canonical)
        guard !c.isEmpty else { return "" }
        return stride(from: 0, to: c.count, by: 4).map { String(c[$0..<$0 + 4]) }.joined(separator: "-")
    }

    public var description: String { "[recovery key]" }
    public var debugDescription: String { "[recovery key]" }
    public var customMirror: Mirror { Mirror(self, children: [], displayStyle: .struct) }

    /// Overwrites the symbols (best effort: copies made by Text or the
    /// pasteboard aren't ours to clear). A wiped key derives nothing.
    public mutating func wipe() {
        for i in symbols.indices { symbols[i] = 0 }
        symbols.removeAll()
    }

    // MARK: Each box's recovery password

    public enum DeriveError: Error, Equatable {
        case noKey
        case invalidBoxID
    }

    /// "box-" and 12 lowercase hex digits.
    public static func isValidBoxID(_ id: String) -> Bool {
        let b = Array(id.utf8)
        guard b.count == 16, b.starts(with: Array("box-".utf8)) else { return false }
        return b[4...].allSatisfy { (0x30...0x39).contains($0) || (0x61...0x66).contains($0) }
    }

    /// HKDF-SHA256 over the canonical key, salt `salt`, info `infoPrefix`,
    /// a zero byte and the box ID; 32 bytes as 64 lowercase hex characters,
    /// the encoding of repository keys. The box ID is checked first.
    public func password(boxID: String) throws -> String {
        guard symbols.count == Self.length else { throw DeriveError.noKey }
        guard Self.isValidBoxID(boxID) else { throw DeriveError.invalidBoxID }
        var ikm = Array(canonical.utf8)
        defer { for i in ikm.indices { ikm[i] = 0 } }
        let info = Array(Self.infoPrefix.utf8) + [0] + Array(boxID.utf8)
        let out = Self.hkdfSHA256(ikm: ikm, salt: Array(Self.salt.utf8), info: info, length: 32)
        return out.map { String(format: "%02x", $0) }.joined()
    }

    /// RFC 5869 HKDF with SHA-256 (CryptoKit).
    static func hkdfSHA256(ikm: [UInt8], salt: [UInt8], info: [UInt8], length: Int) -> [UInt8] {
        let key = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: ikm), salt: salt,
                                         info: info, outputByteCount: length)
        return key.withUnsafeBytes { Array($0) }
    }

    // MARK: The check character

    /// GF(2^5) with x^5 + x^2 + 1; α = x = 2 is primitive (order 31).
    static func gfMul(_ a: UInt8, _ b: UInt8) -> UInt8 {
        var (a, b, r) = (a, b, UInt8(0))
        while b != 0 {
            if b & 1 != 0 { r ^= a }
            b >>= 1
            a <<= 1
            if a & 0x20 != 0 { a ^= 0x25 }
        }
        return r
    }

    /// α^(23−i), the weight of position i.
    static let weights: [UInt8] = (0..<length).map { i in
        (0..<(length - 1 - i)).reduce(UInt8(1)) { w, _ in gfMul(w, 2) }
    }

    /// Σ α^(23−i) · v_i over the values given, from position 0. Over the 23
    /// random symbols it's the check character; over all 24 of a valid key
    /// it's 0.
    static func syndrome(_ values: [UInt8]) -> UInt8 {
        zip(weights, values).reduce(0) { $0 ^ gfMul($1.0, $1.1) }
    }
}
