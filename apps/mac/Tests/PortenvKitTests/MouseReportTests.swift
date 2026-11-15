// SPDX-License-Identifier: Apache-2.0

@testable import PortenvKit
import Testing

/// Which bytes a tab view lets through as the person's mouse (build 257:
/// the wheel did nothing in a full-screen program's tab), and that nothing
/// else the emulator writes looks like one.
struct MouseReportTests {
    static let esc = "\u{1b}"

    @Test func everyEncodingTheEmulatorWritesIsAReport() {
        let reports: [[UInt8]] = [
            Array("\(Self.esc)[<64;10;5M".utf8), // SGR wheel up
            Array("\(Self.esc)[<65;1;1M".utf8), // SGR wheel down
            Array("\(Self.esc)[<0;80;24m".utf8), // SGR release
            Array("\(Self.esc)[<0;1200;800M".utf8), // SGR pixels
            Array("\(Self.esc)[96;10;5M".utf8), // urxvt
            [0x1b, 0x5b, 0x4d, 96, 42, 37], // X10 / normal
            [0x1b, 0x5b, 0x4d, 255, 255, 255], // X10 at its limit
            Array("\(Self.esc)[M`\u{100}\u{80}".utf8), // UTF-8 (1005)
        ]
        for r in reports { #expect(MouseReport.isReport(r), "\(r)") }
    }

    @Test func repliesAndKeysAreNot() {
        let others = [
            "\(Self.esc)[?1;2c", "\(Self.esc)[>0;276;0c", "\(Self.esc)[0n", "\(Self.esc)[24;80R", "\(Self.esc)[?24;80;1R",
            "\(Self.esc)P1$r0m\(Self.esc)\\", "\(Self.esc)[?2004;2$y", "\(Self.esc)[8;24;80t", "\(Self.esc)]11;rgb:0000/0000/0000\u{07}",
            "\(Self.esc)[A", "\(Self.esc)OA", "\(Self.esc)[I", "\(Self.esc)[O", "y", "\r", "",
            "\(Self.esc)[<64;10M", "\(Self.esc)[<64;10;5;1M", "\(Self.esc)[<64;10;5", "\(Self.esc)[<a;1;1M", "\(Self.esc)[<64;10;5Mx",
            "\(Self.esc)[1;1M", "\(Self.esc)[1;1;1;1M", "\(Self.esc)[M  ", "\(Self.esc)[M   x", "\(Self.esc)[<123456;1;1M",
        ]
        for o in others { #expect(!MouseReport.isReport(Array(o.utf8)), "\(o.debugDescription)") }
        #expect(!MouseReport.isReport([0x1b, 0x5b, 0x4d, 31, 40, 40]), "a control byte in X10")
    }
}
