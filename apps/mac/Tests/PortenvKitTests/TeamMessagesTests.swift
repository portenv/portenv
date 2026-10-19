// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The owner sees every message that involves their agents (ADR 0016 point
/// 5, R-0313, R-0314): an agent's Team section, Team activity and the field
/// to message an agent.
struct TeamMessagesTextTests {
    let team = [TeamAgent(id: "lead", name: "Lead", status: .idle), TeamAgent(id: "writer", name: "Writer", status: .running)]
    let utc = TimeZone(identifier: "UTC")!
    /// 20:14 UTC.
    let at = Date(timeIntervalSince1970: 20 * 3600 + 14 * 60)

    func board() -> TeamState {
        var t = TeamState()
        for a in team { t.apply(.agent(a)) }
        for (id, from, to, about) in [("1", "lead", "writer", ""), ("2", "writer", "owner", ""), ("3", "owner", "writer", ""),
                                      ("4", "writer", "lead", ""), ("5", "portenv", "writer", ""), ("6", "portenv", "owner", "writer"),
                                      ("7", "portenv", "lead", ""), ("8", "lead", "owner", "")] {
            t.apply(.message(TeamMessage(id: id, from: from, to: to, kind: from == "portenv" ? .notice : .answer, text: id, about: about)))
        }
        return t
    }

    /// The Team section holds what the conversation doesn't: the agent's
    /// messages with other agents and Portenv's notes to it; never another
    /// agent's.
    @Test func theTeamSectionIsEveryOtherMessageOfThatAgent() {
        let t = board()
        #expect(t.conversation(with: "writer").map(\.id) == ["2", "3", "6"])
        #expect(t.teamMessages(of: "writer").map(\.id) == ["1", "4", "5"])
        #expect(t.teamMessages(of: "lead").map(\.id) == ["1", "4", "7"])
    }

    /// Team activity: the whole board in board order, or one agent's.
    @Test func teamActivityIsTheWholeBoardFilteredByAgent() {
        let t = board()
        #expect(t.activity().map(\.id) == ["1", "2", "3", "4", "5", "6", "7", "8"])
        #expect(t.activity(filter: "writer").map(\.id) == ["1", "2", "3", "4", "5", "6"])
        #expect(t.activity(filter: "lead").map(\.id) == ["1", "4", "7", "8"])
    }

    @Test func entriesReadAsSenderToRecipient() {
        let m = TeamMessage(id: "1", from: "lead", to: "writer", text: "\nTake the retry half.\nThen the docs.", createdAt: at)
        #expect(TeamMessageText.route(m, team: team) == "Lead \u{2192} Writer")
        #expect(TeamMessageText.time(at, timeZone: utc) == "20:14")
        #expect(TeamMessageText.firstLine(m.text) == "Take the retry half.")
        #expect(TeamMessageText.spoken(m, team: team, timeZone: utc) == "Lead to Writer, 20:14: Take the retry half.")
        let notice = TeamMessage(id: "2", from: "portenv", to: "writer", kind: .notice, text: "Check the build", createdAt: at)
        #expect(TeamMessageText.isNotice(notice))
        #expect(TeamMessageText.spoken(notice, team: team, timeZone: utc) == "Notice from Portenv to Writer, 20:14: Check the build")
        let yours = TeamMessage(id: "3", from: "owner", to: "ghost", text: "hi")
        #expect(TeamMessageText.spoken(yours, team: team) == "You to ghost: hi")
    }

    /// A hostile message: markup stays literal text, control and bidi
    /// characters are gone, a 50 KB body is cut to the board's 8 KB, and an
    /// opened entry shows a preview with Show all.
    @Test func aHostileMessageIsShownSafely() {
        let markup = "<b>bold</b> [click](https://evil.example) ![x](file:///etc/passwd) **now**"
        let bidi = "\u{202E}gnp.exe\u{2066}x\u{2069}\u{200F}\u{061C}\u{1b}[2J\u{1b}]8;;https://evil\u{07}"
        let body = String(repeating: "A long line of text.\n", count: 50 * 1024 / 21 + 1)
        let m = TeamMessage(id: "h", from: "lead", to: "writer", text: markup + bidi + "\n" + body)
        #expect(m.text.hasPrefix(markup))
        #expect(!m.text.unicodeScalars.contains { [0x202E, 0x2066, 0x2069, 0x200F, 0x061C, 0x1B, 0x07].contains($0.value) })
        #expect(m.text.count == 8 * 1024)
        #expect(TeamMessageText.firstLine(m.text).hasPrefix(markup))
        let preview = TeamMessageText.preview(m.text)
        #expect(preview != nil && preview!.count <= TeamMessageText.previewCharacters + 1)
        #expect(preview!.split(separator: "\n").count <= TeamMessageText.previewLines)
        #expect(TeamMessageText.preview("short\ntext") == nil)
        #expect(TeamMessageText.preview(String(repeating: "x", count: 600)) == nil)
        #expect(TeamMessageText.preview(String(repeating: "x", count: 601)) == String(repeating: "x", count: 600) + "\u{2026}")
    }

    @Test func overEightKBIsRefusedHere() {
        #expect(TeamMessageText.refusal(String(repeating: "x", count: 8192)) == nil)
        #expect(TeamMessageText.refusal(String(repeating: "x", count: 9420))
            == "A message can be at most 8 KB; this one is 9.2 KB. Shorten it and send again.")
        // Bytes, as portenvd counts them: 3000 × "€" is 9000 bytes.
        #expect(TeamMessageText.refusal(String(repeating: "\u{20AC}", count: 3000)) != nil)
    }
}

@MainActor
struct TeamMessageFieldTests {
    /// The field sends exactly what was typed (line breaks and spaces kept),
    /// to that agent only, and clears; nothing goes for white space.
    @Test func theFieldSendsExactlyTheTypedTextToThatAgent() async {
        let daemon = FakeDaemon()
        let team = TeamController(box: "acme", daemon: daemon)
        team.setDraft("  \n ", for: "writer")
        #expect(await team.submit(to: "writer") == false)
        let typed = "  Ping me first.\nThen run the tests. "
        team.setDraft(typed, for: "writer")
        team.setDraft("for lead", for: "lead")
        #expect(await team.submit(to: "writer"))
        #expect(daemon.calls.filter { $0[1] == "send-message" } == [["app", "send-message", "acme", "writer", typed]])
        #expect(team.draft(for: "writer").isEmpty && team.draft(for: "lead") == "for lead")
        #expect(team.state.conversation(with: "writer").map(\.text) == [typed])
    }

    /// A refusal shows under the field and keeps the draft; typing clears
    /// it. Over 8 KB never reaches portenvd.
    @Test func aRefusalShowsAndKeepsTheDraft() async {
        let daemon = FakeDaemon()
        let team = TeamController(box: "acme", daemon: daemon)
        daemon.failing["send-message"] = "Writer isn\u{2019}t in this workspace\u{2019}s team. Check its agent file."
        team.setDraft("hello", for: "writer")
        #expect(await team.submit(to: "writer") == false)
        #expect(team.refusals["writer"] == "Writer isn\u{2019}t in this workspace\u{2019}s team. Check its agent file.")
        #expect(team.draft(for: "writer") == "hello" && team.error == nil)
        team.setDraft("hello!", for: "writer")
        #expect(team.refusals["writer"] == nil)
        daemon.failing["send-message"] = nil
        team.setDraft(String(repeating: "x", count: 9000), for: "writer")
        #expect(await team.submit(to: "writer") == false)
        #expect(team.refusals["writer"]?.hasPrefix("A message can be at most 8 KB") == true)
        #expect(daemon.calls.filter { $0[1] == "send-message" }.count == 1)
    }

    /// The Team section and Team activity follow WatchTeam live.
    @Test func messagesArriveLive() async {
        let daemon = FakeDaemon()
        let lead = TeamAgent(id: "lead", name: "Lead", status: .running)
        daemon.teamScripts = [
            .init(events: [(.agent(lead), "c1"), (.message(TeamMessage(id: "m1", from: "lead", to: "writer", text: "one")), "c2")], failure: "gone"),
            .init(events: [(.message(TeamMessage(id: "m2", from: "writer", to: "lead", text: "two")), "c3")], failure: nil),
        ]
        let team = TeamController(box: "acme", daemon: daemon, retry: .milliseconds(1))
        let follow = Task { await team.follow() }
        for _ in 0 ..< 200 where team.state.cursor != "c3" { try? await Task.sleep(for: .milliseconds(5)) }
        follow.cancel()
        #expect(team.state.teamMessages(of: "lead").map(\.id) == ["m1", "m2"])
        #expect(team.state.activity(filter: "writer").map(\.text) == ["one", "two"])
    }

    /// Team activity shows every tab and leaves the terminal where it was.
    @Test func teamActivityKeepsTheTabs() async {
        let tabs = TabsController(box: "acme", daemon: FakeDaemon())
        tabs.receive([TabInfo(id: "@1", name: "shell", active: true), TabInfo(id: "lead:@1", name: "lead", active: false, agentID: "lead")])
        await tabs.view(.activity)?.value
        #expect(tabs.shown(for: .activity).count == 2)
        #expect(tabs.terminalTab?.id == "@1")
    }
}
