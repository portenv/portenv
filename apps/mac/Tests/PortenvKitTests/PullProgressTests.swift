// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The first open pulls the workspace image: the state line says how far
/// it has got, from portenvd's state (R-0211, R-0427).
@MainActor
struct PullProgressTests {
    /// Whole MB, decimal (as Finder); the total only once portenvd knows it.
    @Test func theLineSaysHowFar() {
        #expect(StateLine.downloadingImage(done: 120_000_000, total: 283_000_000) == "Downloading the workspace image\u{2026} 120 of 283 MB")
        #expect(StateLine.downloadingImage(done: 120_900_000, total: 283_400_000) == "Downloading the workspace image\u{2026} 120 of 283 MB")
        #expect(StateLine.downloadingImage(done: 1_260_000_000, total: 1_500_000_000) == "Downloading the workspace image\u{2026} 1260 of 1500 MB")
        #expect(StateLine.downloadingImage(done: 0, total: 283_000_000) == "Downloading the workspace image\u{2026} 0 of 283 MB")
        // The total isn't known yet: the bytes alone.
        #expect(StateLine.downloadingImage(done: 120_000_000, total: 0) == "Downloading the workspace image\u{2026} 120 MB")
        // Nothing yet: the words alone.
        #expect(StateLine.downloadingImage(done: 0, total: 0) == "Downloading the workspace image\u{2026}")
        #expect(StateLine.downloadingImage(done: 400_000, total: 0) == "Downloading the workspace image\u{2026}")
        #expect(TitleLook(line: StateLine.downloadingImage(done: 1, total: 2), symbol: "arrow.triangle.2.circlepath", spins: true).consistent)
        #expect(TitleLook(line: StateLine.downloadingImage(done: 120_000_000, total: 0), symbol: "arrow.triangle.2.circlepath", spins: true).consistent)
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
                if let line, line.hasPrefix("Downloading") { seen.lines.append(line); break }
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
        #expect(seen.lines == ["Downloading the workspace image\u{2026} 312 of 900 MB", "Opening\u{2026}"])
        #expect(c.progress == nil)
    }

    /// The window follows the state only once the open is done (AppRoot),
    /// so the open follows it itself meanwhile: the line moves with every
    /// step, and each step counts as activity (`pullSteps`) for the open's
    /// 20 s timer. Its watch ends with the open.
    @Test func theOpenFollowsThePullWithNothingElseFollowing() async throws {
        let daemon = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: daemon)
        final class Seen: @unchecked Sendable { var lines: [String] = []; var steps: [Int] = [] }
        let seen = Seen()
        daemon.onRun = { args in
            guard args == ["app", "open", "acme-api"] else { return }
            for _ in 0 ..< 1000 where daemon.watching == 0 { try? await Task.sleep(for: .milliseconds(5)) }
            for (json, want) in [
                (#"{"state":"SAVE_STATE_CLOSED","image_pull":{}}"#, "Downloading the workspace image\u{2026}"),
                (#"{"state":"SAVE_STATE_CLOSED","image_pull":{"done_bytes":120000000}}"#, "Downloading the workspace image\u{2026} 120 MB"),
                (#"{"state":"SAVE_STATE_CLOSED","image_pull":{"done_bytes":120000000,"total_bytes":283000000}}"#,
                 "Downloading the workspace image\u{2026} 120 of 283 MB"),
                (#"{"state":"SAVE_STATE_CLOSED"}"#, "Opening\u{2026}"),
            ] {
                daemon.push(json)
                for _ in 0 ..< 400 {
                    if await MainActor.run(body: { c.progress }) == want { break }
                    try? await Task.sleep(for: .milliseconds(5))
                }
                let (line, steps) = await MainActor.run { (c.progress ?? "", c.pullSteps) }
                seen.lines.append(line)
                seen.steps.append(steps)
            }
        }
        await c.open()
        #expect(seen.lines == ["Downloading the workspace image\u{2026}", "Downloading the workspace image\u{2026} 120 MB",
                               "Downloading the workspace image\u{2026} 120 of 283 MB", "Opening\u{2026}"])
        #expect(seen.steps == [1, 2, 3, 3], "every step of the pull is activity; its end is not")
        #expect(c.progress == nil)
        // The open's own watch ended with it: a later state isn't applied.
        daemon.push(#"{"state":"SAVE_STATE_CLOSED","image_pull":{"done_bytes":200000000,"total_bytes":283000000}}"#)
        try? await Task.sleep(for: .milliseconds(100))
        #expect(c.pullSteps == 3)
        #expect(c.state?.imagePull == nil)
    }
}
