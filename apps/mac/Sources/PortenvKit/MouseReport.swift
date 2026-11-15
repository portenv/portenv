// SPDX-License-Identifier: Apache-2.0

/// A mouse report, as the terminal emulator writes one for a click, a drag
/// or the wheel when the program in the tab asked for them (xterm's mouse
/// modes 1000–1006, 1015). A tab view passes these to its tab and nothing
/// else the emulator writes by itself: its replies to a program's queries
/// never reach the tab (ADR 0020, R-0133 item 1).
///
/// A full-screen program with mouse reporting on (Claude Code's fullscreen
/// renderer, less with --mouse, vim with mouse=a) scrolls its own output
/// with the wheel; without the reports, the wheel did nothing in its tab
/// (build 257).
public enum MouseReport {
    /// Whether `bytes` are exactly one mouse report:
    /// - X10 and normal: `ESC [ M` and three bytes, each 32 or more;
    /// - UTF-8 (1005): `ESC [ M` and three characters, each 32 or more;
    /// - SGR (1006, 1016): `ESC [ <` b `;` x `;` y, then `M` or `m`;
    /// - urxvt (1015): `ESC [` b `;` x `;` y `M`.
    public static func isReport(_ bytes: [UInt8]) -> Bool {
        guard bytes.count >= 6, bytes[0] == 0x1b, bytes[1] == UInt8(ascii: "[") else { return false }
        let rest = Array(bytes[2...])
        if rest[0] == UInt8(ascii: "M") {
            let coded = Array(rest[1...])
            if coded.count == 3 { return coded.allSatisfy { $0 >= 32 } }
            guard let text = String(validating: coded, as: UTF8.self) else { return false }
            let scalars = Array(text.unicodeScalars)
            return scalars.count == 3 && scalars.allSatisfy { $0.value >= 32 }
        }
        if rest[0] == UInt8(ascii: "<") {
            guard let last = rest.last, last == UInt8(ascii: "M") || last == UInt8(ascii: "m") else { return false }
            return threeNumbers(rest[1 ..< rest.count - 1])
        }
        guard rest.last == UInt8(ascii: "M") else { return false }
        return threeNumbers(rest[0 ..< rest.count - 1])
    }

    /// `n;n;n`, each 1 to 5 digits.
    private static func threeNumbers(_ s: ArraySlice<UInt8>) -> Bool {
        let parts = s.split(separator: UInt8(ascii: ";"), omittingEmptySubsequences: false)
        return parts.count == 3 && parts.allSatisfy { p in
            (1 ... 5).contains(p.count) && p.allSatisfy { (UInt8(ascii: "0") ... UInt8(ascii: "9")).contains($0) }
        }
    }
}
