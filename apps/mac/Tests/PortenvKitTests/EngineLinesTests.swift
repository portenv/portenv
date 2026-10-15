// SPDX-License-Identifier: Apache-2.0

import Foundation
import GRPCCore
import Testing

@testable import PortenvKit

/// portenvd's engine lines (core/daemon/testdata/engine-lines.txt, which
/// its Go test checks against the constants) reach the app's window
/// unchanged through DaemonErrorText, ending with the window's own Try
/// Again (R-0214).
struct EngineLinesTests {
    static var fixture: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("core/daemon/testdata/engine-lines.txt")
    }

    @Test func portenvdsEngineLinesPassThroughUnchanged() throws {
        let lines = try String(contentsOf: Self.fixture, encoding: .utf8).split(separator: "\n").map(String.init)
        #expect(lines.count == 3)
        for line in lines {
            #expect(DaemonErrorText.plain(code: .unavailable, message: line) == line)
            #expect(line.hasSuffix("then click Try Again."))
            #expect(!line.contains("'"))
        }
    }
}
