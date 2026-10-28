// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvProto
import Testing

@testable import PortenvKit

/// The owner's Fetch (ADR 0022 §2a): an agent's shared branch comes into the
/// owner's project as agents/<id> through portenvd's FetchAgentBranch, from
/// the app, with no command line.
@MainActor
struct TeamFetchTests {
    let writer = TeamAgent(id: "writer", name: "Code Writer", status: .idle, ownClone: true)
    let lead = TeamAgent(id: "lead", name: "Engineering Lead", status: .idle, ownClone: false)

    func share(from id: String, branch: String) -> TeamMessage {
        TeamMessage(id: "s-\(id)", from: id, to: TeamText.owner, kind: .notice, text: "Work is ready on \(branch)", sharedBranch: branch)
    }

    /// Fetch shows only for an agent with its own copy that shared its own
    /// branch: on that message, and in its column's controls.
    @Test func fetchShowsOnlyForAClonedAgentsSharedBranch() {
        let shared = share(from: "writer", branch: "agent/writer")
        #expect(TeamFetch.shows(on: shared, for: writer))
        #expect(TeamFetch.canFetch(writer, messages: [shared]))
        // No share yet, or only a message that reads like one.
        let lookalike = TeamMessage(id: "x", from: "writer", to: TeamText.owner, text: "Work is ready on agent/writer")
        #expect(!TeamFetch.shows(on: lookalike, for: writer))
        #expect(!TeamFetch.canFetch(writer, messages: [lookalike]))
        #expect(!TeamFetch.canFetch(writer, messages: []))
        // Another agent's branch, or one not its own.
        #expect(!TeamFetch.shows(on: share(from: "writer", branch: "agent/lead"), for: writer))
        #expect(!TeamFetch.shows(on: share(from: "lead", branch: "agent/writer"), for: writer))
        // An agent with no copy of the project has no branch.
        #expect(!TeamFetch.shows(on: share(from: "lead", branch: "agent/lead"), for: lead))
        #expect(!TeamFetch.canFetch(lead, messages: [share(from: "lead", branch: "agent/lead")]))
    }

    @Test func theWords() {
        #expect(TeamFetch.button == "Fetch")
        #expect(TeamFetch.menuTitle("writer") == "Fetch agent/writer")
        #expect(TeamFetch.fetched(id: "writer", commit: "0123456789abcdef0123456789abcdef01234567")
            == "Fetched agent/writer into your project as agents/writer (0123456789ab). Review it with git, then merge.")
    }

    /// The button calls FetchAgentBranch with the workspace and the agent,
    /// and the agent's column shows the line it answers.
    @Test func fetchCallsPortenvdAndSaysWhatCameIn() async {
        let daemon = FakeDaemon()
        daemon.fetched = ("refs/remotes/agents/writer", "0123456789abcdef0123456789abcdef01234567")
        let team = TeamController(box: "acme-api", daemon: daemon)
        team.receive(.agent(writer))
        await team.fetch("writer")
        #expect(daemon.calls.filter { $0[1] == "fetch-agent" } == [["app", "fetch-agent", "acme-api", "writer"]])
        #expect(team.fetchLine(for: "writer") == FetchLine(text: "Fetched agent/writer into your project as agents/writer (0123456789ab). Review it with git, then merge.", failed: false))
        #expect(team.fetchLine(for: "lead") == nil)
        #expect(!team.isFetching("writer"))
    }

    /// A refusal shows as portenvd says it, plain, in that agent's column.
    @Test func aRefusedFetchShowsItsLine() async {
        let daemon = FakeDaemon()
        daemon.failing["fetch-agent"] = "writer's branch agent/writer has no commits the owner doesn't have yet"
        let team = TeamController(box: "acme-api", daemon: daemon)
        team.receive(.agent(writer))
        await team.fetch("writer")
        #expect(team.fetchLine(for: "writer") == FetchLine(text: "writer's branch agent/writer has no commits the owner doesn't have yet", failed: true))
        // A fetch that then works replaces it.
        daemon.failing["fetch-agent"] = nil
        daemon.fetched = ("refs/remotes/agents/writer", "abcdef0123456789abcdef0123456789abcdef01")
        await team.fetch("writer")
        #expect(team.fetchLine(for: "writer")?.failed == false)
    }

    /// portenvd's own_clone and a share's branch reach the kit.
    @Test func theProtoCarriesTheCloneAndTheBranch() {
        var info = Portenv_Daemon_V1_AgentInfo()
        info.id = "writer"
        info.ownClone = true
        #expect(TeamAgent(info).ownClone)
        var m = Portenv_Types_V1_BoardMessage()
        m.id = "1"
        m.from = "writer"
        m.to = "owner"
        m.sharedBranch = "agent/writer"
        #expect(TeamMessage(m).sharedBranch == "agent/writer")
    }
}
