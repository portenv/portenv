// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Command blocks' model (R-0139, R-0174): blocks from the marks, the fold
/// rules and the words on their headers.
struct CommandBlocksTests {
    let t0 = ISO8601DateFormatter().date(from: "2026-10-14T12:31:00Z")!

    func mark(_ row: Int, _ kind: OverlayRows.Kind, at: Date? = nil) -> OverlayRows.Mark {
        var rows = OverlayRows()
        rows.add(row: row, line: ObjectIdentifier(NSString(string: "\(row)\(kind)")), kind: kind, at: at)
        return rows.marks[0]
    }

    @Test func blocksComeFromTheMarks() {
        let marks = [
            mark(0, .prompt), mark(1, .commandStart, at: t0), mark(2, .commandEnd(exit: 0), at: t0.addingTimeInterval(0.8)),
            mark(2, .prompt), mark(3, .commandStart, at: t0), mark(7, .commandEnd(exit: 1), at: t0.addingTimeInterval(8.4)),
            mark(7, .prompt), mark(8, .commandStart, at: t0),
        ]
        let blocks = CommandBlocks.blocks(marks: marks, cursorRow: 12) { ["git pull", "npm test", "claude"][[0, 2, 7].firstIndex(of: $0)!] }
        #expect(blocks.map(\.command) == ["git pull", "npm test", "claude"])
        #expect(blocks.map(\.state) == [.succeeded, .failed(exit: 1), .running])
        #expect(blocks.map(\.output) == [1 ... 1, 3 ... 6, 8 ... 12])
        #expect(blocks[1].endedAt == t0.addingTimeInterval(8.4))
    }

    /// A prompt with no end mark before it ends the command (the shell lost
    /// the D mark): never a block that runs forever.
    @Test func aLostEndMarkStillEndsTheBlock() {
        let marks = [mark(0, .prompt), mark(1, .commandStart), mark(4, .prompt)]
        let blocks = CommandBlocks.blocks(marks: marks, cursorRow: 4) { _ in "x" }
        #expect(blocks.first?.state == .succeeded)
        #expect(blocks.first?.output == 1 ... 3)
        #expect(blocks.first?.endedAt == nil)
    }

    /// R-0174: a success folds by default however long; a failure and a
    /// running block open by themselves.
    @Test func whatOpensByItself() {
        #expect(!BlockLayout.openByDefault(.succeeded))
        #expect(BlockLayout.openByDefault(.failed(exit: 1)))
        #expect(BlockLayout.openByDefault(.running))
    }

    /// An open long block keeps its first and last 20 lines until Show All
    /// Output, success or failure alike; a short one shows everything.
    @Test func anOpenLongBlockKeepsTwentyAtEachEnd() {
        let long = CommandBlock(id: 10, command: "npm run build", state: .succeeded, output: 10 ... 2409)
        #expect(BlockLayout.shown(long, open: false, showAll: false) == .folded)
        #expect(BlockLayout.shown(long, open: true, showAll: false) == .headTail(head: 10 ... 29, hidden: 2360, tail: 2390 ... 2409))
        #expect(BlockLayout.shown(long, open: true, showAll: true) == .all(10 ... 2409))
        let failedLong = CommandBlock(id: 10, command: "make", state: .failed(exit: 2), output: 10 ... 109)
        #expect(BlockLayout.shown(failedLong, open: true, showAll: false) == .headTail(head: 10 ... 29, hidden: 60, tail: 90 ... 109))
        let short = CommandBlock(id: 3, command: "npm test", state: .failed(exit: 1), output: 3 ... 42)
        #expect(BlockLayout.shown(short, open: true, showAll: false) == .all(3 ... 42))
        let silent = CommandBlock(id: 3, command: "true", state: .succeeded, output: nil)
        #expect(BlockLayout.shown(silent, open: true, showAll: false) == .folded)
    }

    @Test func theHeadersWords() {
        let tz = TimeZone(identifier: "Europe/Paris")!
        let gb = Locale(identifier: "en_GB"), en = Locale(identifier: "en")
        let ok = CommandBlock(id: 1, command: "git pull", state: .succeeded, output: 1 ... 1, startedAt: t0, endedAt: t0.addingTimeInterval(0.8))
        #expect(BlockText.status(ok, now: t0, timeZone: tz, clockLocale: gb, locale: en) == "0.8 s · 14:31")
        let failed = CommandBlock(id: 1, command: "npm test", state: .failed(exit: 1), output: 1 ... 4, startedAt: t0, endedAt: t0.addingTimeInterval(8.4))
        #expect(BlockText.status(failed, now: t0, timeZone: tz, clockLocale: gb, locale: en) == "exit 1 · 8.4 s · 14:31")
        let running = CommandBlock(id: 1, command: "claude", state: .running, output: nil, startedAt: t0)
        #expect(BlockText.status(running, now: t0.addingTimeInterval(190), timeZone: tz, clockLocale: gb, locale: en) == "running · 3 min · since 14:31")
        // Times the app never saw (a seeded block) are left out, never guessed.
        let seeded = CommandBlock(id: 1, command: "ls", state: .failed(exit: 2), output: 1 ... 1)
        #expect(BlockText.status(seeded, now: t0, timeZone: tz, clockLocale: gb, locale: en) == "exit 2")
        #expect(BlockText.elapsed(41.7, locale: en) == "41 s")
        #expect(BlockText.elapsed(7500, locale: en) == "2 h 5 min")
    }

    /// The count is in the app's language: "2,398" on a French Mac too.
    @Test func theHiddenCountFollowsTheAppsLanguage() {
        #expect(BlockText.hidden(2398, locale: Locale(identifier: "en")) == "\u{2026} 2,398 lines hidden")
        #expect(BlockText.hidden(2398) == "\u{2026} 2,398 lines hidden")
    }
}
