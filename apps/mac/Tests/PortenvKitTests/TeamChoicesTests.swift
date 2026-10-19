// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit
import PortenvProto

/// An ask-owner with choices (CC-0501): the app answers with the choice's
/// number and its label exactly as portenvd sent them; the card shows each
/// label as plain text; a yes/no question keeps Approve and Decline.
@MainActor
struct TeamChoicesTests {
    static let labels = ["Option a: wait", "Option b: install deps", "Option c: skip"]

    func controller(_ daemon: FakeDaemon, decision: TeamDecision) async -> TeamController {
        let builder = TeamAgent(id: "builder", name: "Builder", status: .needsYou, approved: true, runner: "claude")
        daemon.teamScripts = [.init(events: [(.agent(builder), "c1"), (.decision(decision), "c2")], failure: DaemonError.outOfDate)]
        let team = TeamController(box: "acme", daemon: daemon)
        await team.follow()
        return team
    }

    @Test func choosingSendsTheNumberAndTheExactLabel() async {
        let daemon = FakeDaemon()
        let ask = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "What now?", choices: Self.labels)
        let team = await controller(daemon, decision: ask)
        #expect(team.decisions(for: "builder").map(\.id) == ["d1"])
        var chose = ask
        chose.state = .approved
        chose.choice = 2
        daemon.answered = ["d1": chose]
        await team.choose(ask, index: 1)
        #expect(daemon.calls.filter { $0[1] == "choose-decision" } == [["app", "choose-decision", "acme", "d1", "2", "Option b: install deps"]])
        #expect(!daemon.calls.contains { $0[1] == "answer-decision" })
        #expect(team.error == nil)
        #expect(team.decisions(for: "builder").isEmpty)
        #expect(team.state.decisions.first?.chosen == "Option b: install deps")
        #expect(TeamText.answered(team.state.decisions[0], team: []) == "Chose \u{201C}Option b: install deps\u{201D}")
    }

    @Test func aChoiceOutOfRangeOrAnsweredIsNeverSent() async {
        let daemon = FakeDaemon()
        let ask = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "What now?", choices: Self.labels)
        let team = await controller(daemon, decision: ask)
        await team.choose(ask, index: 3)
        await team.choose(ask, index: -1)
        var expired = ask
        expired.state = .expired
        await team.choose(expired, index: 0)
        let yesNo = TeamDecision(id: "d2", from: "builder", kind: .askOwner, summary: "May I?")
        await team.choose(yesNo, index: 0)
        #expect(!daemon.calls.contains { $0[1] == "choose-decision" })
    }

    /// portenvd's refusal (a label that isn't the decision's) is the
    /// owner's error line, and the decision stays pending.
    @Test func aRefusedChoiceSaysWhy() async {
        let daemon = FakeDaemon()
        let ask = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "What now?", choices: Self.labels)
        let team = await controller(daemon, decision: ask)
        daemon.failingOnce["choose-decision"] = "that choice isn't the request's; it may have changed, so look again"
        await team.choose(ask, index: 0)
        #expect(team.error == "that choice isn't the request's; it may have changed, so look again")
        #expect(team.decisions(for: "builder").map(\.id) == ["d1"])
    }

    /// The labels are sent back as portenvd sent them, but shown as plain
    /// text on one line: no control or bidi characters, at most 80
    /// characters, and at most 4 choices.
    @Test func labelsAreShownAsPlainText() {
        let raw = ["two  spaces", "bidi\u{202E}txet", "esc\u{1B}[31mred", String(repeating: "x", count: 90), "fifth"]
        let d = TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "Which?", choices: raw)
        #expect(d.choices == Array(raw.prefix(4)))
        #expect(d.shownChoices == ["two spaces", "biditxet", "esc[31mred", String(repeating: "x", count: 80)])
        #expect(d.chosen == nil)
    }

    @Test func voiceOverSaysWhatEachChoiceAnswers() {
        #expect(TeamText.choose("Option b: install deps", summary: "What now?") == "Choose \u{201C}Option b: install deps\u{201D}: What now?")
    }

    /// From portenvd's decision: its choices and the owner's choice.
    @Test func theProtoCarriesTheChoices() {
        let p = Portenv_Types_V1_Decision.with {
            $0.id = "d1"; $0.from = "builder"; $0.kind = .askOwner; $0.state = .approved
            $0.choices = Self.labels; $0.choice = 3
        }
        let d = TeamDecision(p)
        #expect(d.choices == Self.labels)
        #expect(d.choice == 3)
        #expect(d.chosen == "Option c: skip")
        let yesNo = TeamDecision(Portenv_Types_V1_Decision.with { $0.id = "d2"; $0.kind = .askOwner; $0.state = .approved })
        #expect(yesNo.choices.isEmpty && yesNo.chosen == nil)
        #expect(TeamText.answered(yesNo, team: []) == "Approved")
    }
}
