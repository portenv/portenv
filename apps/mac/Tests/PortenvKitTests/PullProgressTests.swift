// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The first open pulls the workspace image: the state line says how far
/// it has got, from portenvd's state (R-0211).
@MainActor
struct PullProgressTests {
    @Test func theLineSaysHowFar() {
        #expect(StateLine.gettingImage(done: 312_000_000, total: 900_000_000) == "Getting the workspace image\u{2026} 312 MB of 900 MB")
        #expect(StateLine.gettingImage(done: 1_260_000_000, total: 1_500_000_000) == "Getting the workspace image\u{2026} 1.3 GB of 1.5 GB")
        #expect(StateLine.gettingImage(done: 0, total: 0) == "Getting the workspace image\u{2026}")
        #expect(TitleLook(line: StateLine.gettingImage(done: 1, total: 2), symbol: "arrow.triangle.2.circlepath", spins: true).consistent)
    }

    @Test func theStateCarriesThePull() throws {
        let st = try #require(BoxState.parse(#"{"state":"SAVE_STATE_CLOSED","image_pull":{"done_bytes":312000000,"total_bytes":900000000}}"#))
        #expect(st.imagePull == BoxState.ImagePull(done: 312_000_000, total: 900_000_000))
        #expect(BoxState.parse(#"{"state":"SAVE_STATE_CLOSED"}"#)?.imagePull == nil)
    }

    /// While the open runs, a pushed state with a pull moves the line from
    /// "Opening…" to the pull, and back once the pull ends.
    @Test func theOpenShowsThePull() async throws {
        let daemon = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: daemon)
        let follow = Task { await c.follow() }
        defer { follow.cancel() }
        final class Seen: @unchecked Sendable { var lines: [String] = [] }
        let seen = Seen()
        daemon.onRun = { args in
            guard args == ["app", "open", "acme-api"] else { return }
            daemon.push(#"{"state":"SAVE_STATE_CLOSED","image_pull":{"done_bytes":312000000,"total_bytes":900000000}}"#)
            for _ in 0 ..< 200 {
                let line = await MainActor.run { c.progress }
                if let line, line.hasPrefix("Getting") { seen.lines.append(line); break }
                try? await Task.sleep(for: .milliseconds(5))
            }
            daemon.push(#"{"state":"SAVE_STATE_CLOSED"}"#)
            for _ in 0 ..< 200 {
                if await MainActor.run(body: { c.progress }) == "Opening\u{2026}" { seen.lines.append("Opening\u{2026}"); break }
                try? await Task.sleep(for: .milliseconds(5))
            }
        }
        while daemon.watching == 0 { await Task.yield() }
        await c.open()
        #expect(seen.lines == ["Getting the workspace image\u{2026} 312 MB of 900 MB", "Opening\u{2026}"])
        #expect(c.progress == nil)
    }
}
