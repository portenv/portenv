// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// A tab view's guards (ADR 0020, decision 9; R-0133, R-0161): the filter
/// in front of what it's fed, cleaned paste, ⌘-click through TerminalLinks,
/// and a delegate nothing can replace.
@MainActor
struct TabViewGuardTests {
    func view() -> (MarkedTerminalView, ScriptedTabSource) {
        _ = NSApplication.shared
        let v = TerminalViews.tab()
        v.frame = NSRect(x: 0, y: 0, width: 800, height: 480)
        let s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        return (v, s)
    }

    /// The spike's recorded pane output (tests/spike/q3/fixtures).
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

    func line(_ v: MarkedTerminalView, _ row: Int) -> String {
        let text = v.getTerminal().getLine(row: row)?.translateToString(trimRight: true) ?? ""
        return String(text.reversed().drop { $0 == " " }.reversed())
    }

    /// A delegate that records whatever SwiftTerm hands it.
    final class Spy: TerminalViewDelegate {
        var calls: [String] = []
        func sizeChanged(source _: TerminalView, newCols _: Int, newRows _: Int) {}
        func setTerminalTitle(source _: TerminalView, title: String) { calls.append("title \(title)") }
        func hostCurrentDirectoryUpdate(source _: TerminalView, directory: String?) { calls.append("cwd \(directory ?? "")") }
        func send(source _: TerminalView, data _: ArraySlice<UInt8>) { calls.append("send") }
        func scrolled(source _: TerminalView, position _: Double) {}
        func requestOpenLink(source _: TerminalView, link: String, params _: [String: String]) { calls.append("link \(link)") }
        func bell(source _: TerminalView) {}
        func clipboardCopy(source _: TerminalView, content _: Data) { calls.append("clipboard") }
        func clipboardRead(source _: TerminalView) -> Data? { calls.append("clipboard read"); return nil }
        func iTermContent(source _: TerminalView, content _: ArraySlice<UInt8>) { calls.append("iterm") }
        func rangeChanged(source _: TerminalView, startY _: Int, endY _: Int) {}
    }

    @Test func whatTheViewIsFedGoesThroughTheFilter() {
        let (v, _) = view()
        v.ingest(Array("\u{1b}]2;evil title\u{07}\u{1b}]52;c;aGk=\u{07}\u{1b}Pq#0~~\u{1b}\\shown".utf8))
        #expect(line(v, 0) == "shown")
    }

    /// Replacing the delegate is caught and undone before the next byte is
    /// fed, so a program never reaches anything but the view's own relay.
    @Test func theDelegateCantBeReplaced() {
        let (v, s) = view()
        var replaced = 0
        let old = MarkedTerminalView.delegateReplaced
        MarkedTerminalView.delegateReplaced = { replaced += 1 }
        defer { MarkedTerminalView.delegateReplaced = old }
        let spy = Spy()
        v.terminalDelegate = spy
        v.ingest(Array("\u{1b}]7;file:///x\u{07}\u{1b}[6n\u{1b}]8;;https://e.com\u{07}x\u{1b}]8;;\u{07}".utf8))
        #expect(replaced == 1)
        #expect(v.terminalDelegate !== spy)
        #expect(spy.calls.isEmpty, "\(spy.calls)")
        #expect(s.sent.isEmpty)
        // Typing still reaches the tab, through the relay.
        v.send(txt: "y")
        #expect(s.sent.flatMap { $0 } == Array("y".utf8))
    }

    /// A reset starts the filter afresh too: output after an unterminated
    /// DCS (hidden until its ESC \) shows again from the new seed.
    @Test func aResetClearsTheFiltersState() async {
        let (v, s) = view()
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array("\u{1b}Pq never ends".utf8)))
        s.play(.output(Array("hidden".utf8)))
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array("shown".utf8)))
        for _ in 0 ..< 200 where line(v, 0) != "shown" { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(line(v, 0) == "shown")
    }

    /// Text can't get in by being dropped on the view or sent by a Service:
    /// only typing and paste(text:) (R-0167).
    @Test func noDropsOrServices() {
        let (v, _) = view()
        v.registerForDraggedTypes([.string, .fileURL])
        #expect(v.registeredDraggedTypes.isEmpty)
        #expect(v.validRequestor(forSendType: nil, returnType: .string) == nil)
        #expect(v.validRequestor(forSendType: .string, returnType: .string) == nil)
    }

    /// The delegate check runs in the relay's link, title and clipboard
    /// paths too (R-0167).
    @Test func theDelegateCheckRunsInTheRelaysPaths() {
        let (v, _) = view()
        var replaced = 0
        let old = MarkedTerminalView.delegateReplaced
        MarkedTerminalView.delegateReplaced = { replaced += 1 }
        defer { MarkedTerminalView.delegateReplaced = old }
        let relay = v.terminalDelegate
        for call in [{ relay?.setTerminalTitle(source: v, title: "x") }, { relay?.clipboardCopy(source: v, content: Data()) },
                     { relay?.requestOpenLink(source: v, link: "https://e.com", params: [:]) }] {
            v.terminalDelegate = Spy()
            call()
            #expect(v.terminalDelegate === relay)
        }
        #expect(replaced == 3)
    }

    @Test func pasteIsCleanedAndBracketed() {
        let (v, s) = view()
        v.paste(text: "ls\u{1b}[201~rm -rf ~\r\u{00}\u{1b}x")
        #expect(s.sent.flatMap { $0 } == Array("lsrm -rf ~\rx".utf8))
        // With bracketed paste on, the cleaned text is framed once.
        v.ingest(Array("\u{1b}[?2004h".utf8))
        let before = s.sent.count
        v.paste(text: "a\u{1b}[201~b")
        let sent = s.sent[before...].flatMap { $0 }
        #expect(sent == Array("\u{1b}[200~ab\u{1b}[201~".utf8))
    }

    /// A ⌘-clicked link in a tab view goes through TerminalLinks, as in the
    /// attached terminal (R-0161).
    @Test func aClickedLinkGoesThroughTerminalLinks() {
        let (v, _) = view()
        var opened: [URL] = [], refused: [String] = []
        v.links.open = { opened.append($0) }
        v.links.onRefusedLink = { refused.append($0) }
        v.links.isPersonsClick = { _ in true }
        for link in ["https://аpple.com/x", "file:///etc/passwd", "https://apple.com@evil.com/", "https://e.com/\u{202E}x"] {
            v.terminalDelegate?.requestOpenLink(source: v, link: link, params: [:])
        }
        #expect(opened == [URL(string: "https://xn--pple-43d.com/x")!])
        #expect(refused == ["Portenv opens only web links: file:///etc/passwd",
                            "Portenv won't open a link with a user name before the address. It goes to evil.com.",
                            "Portenv won't open a link with hidden characters"])
        // Not the person's click: nothing opens.
        v.links.isPersonsClick = { _ in false }
        v.terminalDelegate?.requestOpenLink(source: v, link: "https://example.com", params: [:])
        #expect(opened.count == 1)
    }

    /// The spike's real streams, mutated with hostile sequences, fed to the
    /// view: nothing reaches the tab, the clipboard, a title or a link, and
    /// the scrollback stays under its cap.
    @Test func fuzzTheViewWithMutatedFixtures() {
        let pb = NSPasteboard.general
        let count = pb.changeCount
        let (v, s) = view()
        let hostile = ["\u{1b}]52;c;aGk=\u{07}", "\u{1b}]52;c;?\u{07}", "\u{1b}]0;t\u{07}", "\u{1b}[21t", "\u{1b}[c", "\u{1b}[6n",
                       "\u{1b}P$qm\u{1b}\\", "\u{1b}]1337;File=:aGk=\u{07}", "\u{1b}c", "\u{1b}]8;;file:///x\u{07}y\u{1b}]8;;\u{07}"]
            .map { Array($0.utf8) } + [[0x9B, 0x36, 0x6E], [0xC2, 0x9D]]
        var state: UInt64 = 0x2545_F491_4F6C_DD1D
        func next() -> Int { state ^= state << 13; state ^= state >> 7; state ^= state << 17; return Int(state % 1_000_000) }
        for stream in Self.fixtureStreams() {
            var bytes = stream
            for _ in 0 ..< 60 where !bytes.isEmpty {
                bytes.insert(contentsOf: hostile[next() % hostile.count], at: next() % bytes.count)
            }
            var i = 0
            while i < bytes.count {
                let n = 1 + next() % 300
                v.ingest(Array(bytes[i ..< min(bytes.count, i + n)]))
                i += n
            }
        }
        #expect(s.sent.isEmpty, "\(s.sent.count) replies reached the tab")
        #expect(pb.changeCount == count)
        #expect(v.tracker.lastValid - v.tracker.firstValid + 1 <= MarkedTerminalView.scrollbackLines + v.getTerminal().rows)
    }
}
