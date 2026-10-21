// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
import Testing

@testable import PortenvKit

/// The owner's agent limit (R-0347): Settings › Team › Agents allowed, the
/// Team section's line, and the owner-confirmed Raise.
@MainActor
struct TeamLimitTests {
    @Test func agentsAllowedIsOneToEightAndFourByDefault() {
        #expect(AgentsAllowed.range == 1 ... 8)
        #expect(AgentsAllowed.defaultValue == 4)
        #expect(AgentsAllowed.choices == [1, 2, 3, 4, 5, 6, 7, 8])
        #expect(SettingsText.agentsAllowed == "Agents allowed")
        #expect(SettingsText.agentsAllowedLine.contains("1 to 8"))
        #expect(SettingsText.agentsAllowedLine.contains("Lowering it stops no agent"))
    }

    /// The limit comes from portenvd, in WatchTeam, and is folded in.
    @Test func theStreamCarriesTheLimit() throws {
        var r = Portenv_Daemon_V1_WatchTeamResponse()
        r.teamLimit = .with {
            $0.agentsAllowed = 4; $0.agentsInTeam = 6; $0.agentsRunning = 2
            $0.line = "This team has 6 agents, and you allow 4 in a workspace: only 4 can run at once."
            $0.raiseTo = 6
        }
        let event = try #require(TeamEvent(r))
        var state = TeamState()
        state.apply(event)
        let limit = try #require(state.limit)
        #expect(limit == TeamLimit(allowed: 4, inTeam: 6, running: 2, line: r.teamLimit.line, raiseTo: 6))
        #expect(state.cursor.isEmpty)
    }

    /// Over the limit: a warning with the one-click Raise, which asks the
    /// owner first. At or near: the line alone, quieter. Under: nothing.
    @Test func theColumnWarningAndItsRaise() {
        let over = TeamLimit(allowed: 4, inTeam: 6, running: 0, line: "This team has 6 agents…", raiseTo: 6)
        #expect(over.shows && over.isWarning)
        #expect(over.raiseTitle == "Raise to 6\u{2026}")
        #expect(over.confirmTitle == "Allow 6 agents in each workspace?")
        #expect(over.confirmMessage == "Agents allowed in Settings \u{203A} Team goes from 4 to 6, for every workspace on this Mac. You can lower it there.")
        #expect(over.confirmButton == "Allow 6")

        let at = TeamLimit(allowed: 4, inTeam: 4, running: 4, line: "4 of 4 agents allowed: the team is at your limit.", raiseTo: 0)
        #expect(at.shows && !at.isWarning && at.raiseTitle == nil)

        let lowered = TeamLimit(allowed: 3, inTeam: 5, running: 5, line: "5 agents are running, and you now allow 3.", raiseTo: 5)
        #expect(lowered.isWarning && lowered.raiseTitle == "Raise to 5\u{2026}")

        let under = TeamLimit(allowed: 4, inTeam: 2, running: 0, line: "", raiseTo: 0)
        #expect(!under.shows)
    }

    /// The Raise goes to portenvd, which enforces the limit; it's the same
    /// call Settings and, later, the New-workspace Team step make.
    @Test func raisingAsksPortenvd() async throws {
        let daemon = FakeDaemon()
        let team = TeamController(box: "acme", daemon: daemon)
        await team.raiseAgentLimit(to: 6)
        #expect(daemon.calls.contains(["app", "set-agents-allowed", "6"]))
        #expect(team.limitError == nil)
        daemon.failing["set-agents-allowed"] = "Agents allowed must be 1 to 8; 9 isn't. Choose a number from 1 to 8"
        await team.raiseAgentLimit(to: 9)
        #expect(team.limitError == "Agents allowed must be 1 to 8; 9 isn't. Choose a number from 1 to 8")
        #expect(try await AgentsAllowed.set(5, daemon: FakeDaemon()) == 5)
    }
}
