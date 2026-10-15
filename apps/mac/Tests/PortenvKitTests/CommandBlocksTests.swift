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
        // When it started isn't said twice: it's in the tooltip and VoiceOver (R-0207).
        #expect(BlockText.status(running, now: t0.addingTimeInterval(190), timeZone: tz, clockLocale: gb, locale: en) == "running · 3 min")
        #expect(BlockText.since(running, timeZone: tz, clockLocale: gb) == "since 14:31")
        #expect(BlockText.since(ok, timeZone: tz, clockLocale: gb) == nil)
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

    // MARK: Slice 2 (R-0207)

    /// VoiceOver reads a header as a button: the command, how it ended and
    /// how long it took; expanded or collapsed is its value.
    @Test func whatVoiceOverReads() {
        let tz = TimeZone(identifier: "Europe/Paris")!, gb = Locale(identifier: "en_GB")
        let build = CommandBlock(id: 1, command: "npm run build", state: .succeeded, output: 1 ... 2400, startedAt: t0, endedAt: t0.addingTimeInterval(41))
        #expect(BlockText.spoken(build, now: t0) == "npm run build, succeeded, 41 seconds")
        #expect(BlockText.expandedState(open: false) == "collapsed")
        #expect(BlockText.expandedState(open: true) == "expanded")
        let failed = CommandBlock(id: 2, command: "npm test -- retries", state: .failed(exit: 1), output: 3 ... 6, startedAt: t0, endedAt: t0.addingTimeInterval(8.4))
        #expect(BlockText.spoken(failed, now: t0) == "npm test -- retries, failed, exit 1, 8.4 seconds")
        let running = CommandBlock(id: 3, command: "claude --continue", state: .running, output: nil, startedAt: t0)
        #expect(BlockText.spoken(running, now: t0.addingTimeInterval(190), timeZone: tz, clockLocale: gb) == "claude --continue, running, 3 minutes, since 14:31")
        let seeded = CommandBlock(id: 4, command: "ls", state: .succeeded, output: 5 ... 5)
        #expect(BlockText.spoken(seeded, now: t0) == "ls, succeeded")
        #expect(BlockText.spokenElapsed(1) == "1 second")
        #expect(BlockText.spokenElapsed(5) == "5 seconds")
        #expect(BlockText.spokenElapsed(60) == "1 minute")
        #expect(BlockText.spokenElapsed(7500) == "2 hours 5 minutes")
        #expect(BlockText.spokenElapsed(7200) == "2 hours")
    }

    /// ⌘↑ and ⌘↓: with nothing selected, ⌘↑ picks the newest block and ⌘↓
    /// the oldest; they stop at the ends.
    @Test func movingBetweenBlocks() {
        let blocks = [1, 5, 9].map { CommandBlock(id: $0, command: "x", state: .succeeded, output: nil) }
        #expect(BlockNavigation.previous(from: nil, in: blocks) == 9)
        #expect(BlockNavigation.next(from: nil, in: blocks) == 1)
        #expect(BlockNavigation.previous(from: 5, in: blocks) == 1)
        #expect(BlockNavigation.next(from: 5, in: blocks) == 9)
        #expect(BlockNavigation.previous(from: 1, in: blocks) == 1)
        #expect(BlockNavigation.next(from: 9, in: blocks) == 9)
        #expect(BlockNavigation.next(from: nil, in: []) == nil)
    }

    /// Find covers every command and every output row, folded or hidden
    /// ones too, case-insensitively and in order.
    @Test func findCoversCommandsAndEveryRow() {
        let rows: [Int: String] = [2: "Retries a payment", 3: "ok", 10: "a retry, another RETRY"]
        let blocks = [
            CommandBlock(id: 1, command: "npm test -- retries", state: .failed(exit: 1), output: 2 ... 3),
            CommandBlock(id: 9, command: "npm run build", state: .succeeded, output: 10 ... 10),
        ]
        let found = BlockFind.matches("retr", in: blocks) { rows[$0] }
        #expect(found == [
            BlockFind.Match(block: 1, row: nil, range: 11 ..< 15),
            BlockFind.Match(block: 1, row: 2, range: 0 ..< 4),
            BlockFind.Match(block: 9, row: 10, range: 2 ..< 6),
            BlockFind.Match(block: 9, row: 10, range: 17 ..< 21),
        ])
        #expect(BlockFind.matches("", in: blocks) { rows[$0] }.isEmpty)
        #expect(BlockFind.count(current: 0, total: 4) == "1 of 4")
        #expect(BlockFind.count(current: nil, total: 0) == "Not found")
    }
}
