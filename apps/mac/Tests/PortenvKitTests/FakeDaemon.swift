// SPDX-License-Identifier: Apache-2.0

import Foundation

@testable import PortenvKit

/// Answers like portenvd would. Calls are recorded as ["app", action,
/// arguments…] (the shape the CLI helper had), `state` is what portenvd
/// records (JSON, as `portenv app state` prints it), and the test can push
/// states into `watch`.
final class FakeDaemon: DaemonAPI, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [[String]] = []
    private var _state = #"{"state":"SAVE_STATE_NOT_SAVED_YET"}"#
    private var _provided: [[String: Data]] = []
    private var watchers: [AsyncThrowingStream<BoxState, Error>.Continuation] = []

    var calls: [[String]] { lock.withLock { _calls } }
    private var _states: [String: String] = [:]
    /// One box's state, where it differs from `state`.
    func setState(_ box: String, _ json: String) { lock.withLock { _states[box] = json } }
    var state: String {
        get { lock.withLock { _state } }
        set { lock.withLock { _state = newValue } }
    }
    /// Keys handed over with provide-keys, in order.
    var provided: [[String: Data]] { lock.withLock { _provided } }
    /// Actions that fail with this message, every time.
    var failing: [String: String] = [:]
    /// Boxes whose close portenvd refuses: a Move To is in progress.
    var movingFor: Set<String> = []
    /// "action box" pairs that fail every time (one box's close, say).
    var failingFor: Set<String> = []
    /// Actions that fail only the first time.
    var failingOnce: [String: String] = [:]
    /// The time revert reports for the restored save point.
    var revertedTo: Date?
    /// Whether woke reports a box restarted.
    var wokeRestarted = false
    /// Runs before each answer (to look at the app mid-call).
    var onRun: (@Sendable ([String]) async -> Void)?

    private func record(_ args: [String]) async throws {
        lock.withLock { _calls.append(args) }
        await onRun?(args)
        let action = args.count > 1 ? args[1] : ""
        if let message = lock.withLock({ failingOnce.removeValue(forKey: action) }) {
            throw DaemonError(message, unavailable: message.contains("not running"))
        }
        if let message = lock.withLock({ failing[action] }) {
            throw DaemonError(message, unavailable: message.contains("not running"))
        }
        if args.count > 2, lock.withLock({ failingFor.contains(action + " " + args[2]) }) {
            throw DaemonError("the workspace helper is unavailable")
        }
        if args.count > 2, lock.withLock({ movingFor.contains(args[2]) }), action == "close" {
            var refused = DaemonError("\(args[2]) is moving to test-server. Close it once the move has finished.")
            refused.moving = true
            throw refused
        }
    }

    /// How many watch streams are open.
    var watching: Int { lock.withLock { watchers.count } }

    /// Pushes a state (JSON) to every watcher.
    func push(_ json: String) {
        guard let s = BoxState.parse(json) else { return }
        lock.withLock { watchers }.forEach { $0.yield(s) }
    }

    /// Ends every watch stream, as when portenvd goes away.
    func endWatches() {
        let all = lock.withLock { let w = watchers; watchers = []; return w }
        all.forEach { $0.finish(throwing: DaemonError("portenvd is not running", unavailable: true)) }
    }

    func ping() async -> Bool { (try? await record(["app", "ping"])) != nil }
    /// What engineRunning answers (nil: portenvd doesn't).
    var engine: Bool? {
        get { lock.withLock { _engine } }
        set { lock.withLock { _engine = newValue } }
    }
    private var _engine: Bool? = true
    func engineRunning() async -> Bool? {
        guard (try? await record(["app", "engine"])) != nil else { return nil }
        return engine
    }
    /// The boxes ListBoxes reports.
    var boxNames: [String] = ["acme-api"]
    func boxes() async throws -> [String] { try await record(["app", "boxes"]); return boxNames }
    func servers() async throws -> [String] { try await record(["app", "servers"]); return ["portenv@server-a"] }
    /// While set, open answers as portenvd does for a workspace open
    /// somewhere else, and placement gives this (answers withheld without
    /// the probe).
    var openElsewhere: Placement? {
        get { lock.withLock { _openElsewhere } }
        set { lock.withLock { _openElsewhere = newValue } }
    }
    private var _openElsewhere: Placement?
    /// Runs at each placement call, with whether it probes.
    var onPlacement: (@Sendable (Bool) async -> Void)?

    func open(_ box: String) async throws {
        try await record(["app", "open", box])
        if openElsewhere != nil { throw DaemonError("open elsewhere", openElsewhere: true) }
    }
    func open(_ box: String, takeOver: Bool) async throws { try await record(["app", "open", box] + (takeOver ? ["--take-over"] : [])) }
    func connectThere(_ box: String) async throws { try await record(["app", "open", box, "--connect-there"]) }
    func placement(_ box: String, probe: Bool) async throws -> Placement {
        try await record(["app", "placement", box] + (probe ? ["--probe"] : []))
        await onPlacement?(probe)
        guard let p = openElsewhere else { return Placement(holder: .none, newestSaveAt: nil, answers: nil) }
        if case .knownServer = p.holder, !probe { return Placement(holder: p.holder, newestSaveAt: p.newestSaveAt, answers: nil) }
        return p
    }
    func close(_ box: String) async throws { try await record(["app", "close", box]) }
    func makeSavePoint(_ box: String) async throws { try await record(["app", "point", box]) }
    func revert(_ box: String) async throws -> Date? { try await record(["app", "revert", box]); return revertedTo }
    func move(_ box: String, to target: String) async throws { try await record(["app", "move", box, target]) }
    func check(_ box: String) async throws -> Bool { try await record(["app", "check", box]); return true }
    func restart(_ box: String) async throws { try await record(["app", "restart", box]) }
    func state(_ box: String) async throws -> BoxState {
        try await record(["app", "state", box])
        let json = lock.withLock { _states[box] } ?? state
        guard let s = BoxState.parse(json) else { throw DaemonError("bad state") }
        return s
    }
    func watch(_ box: String) -> AsyncThrowingStream<BoxState, Error> {
        lock.withLock { _calls.append(["app", "watch", box]) }
        let first = BoxState.parse(state)
        return AsyncThrowingStream { continuation in
            if let failure = lock.withLock({ failing["watch"] }) {
                continuation.finish(throwing: DaemonError(failure, unavailable: true))
                return
            }
            if let first { continuation.yield(first) }
            lock.withLock { watchers.append(continuation) }
        }
    }
    func keyIDs(_ box: String) async throws -> [String] { try await record(["app", "key-ids", box]); return ["box-1", "box-1-storage"] }
    func provideKeys(_ box: String, _ keys: [String: Data]) async throws {
        try await record(["app", "provide-keys", box])
        lock.withLock { _provided.append(keys) }
    }
    func retryPackages(_ box: String) async throws { try await record(["app", "retry-packages", box]) }
    /// What saves answers.
    var saves: [SaveInfo] {
        get { lock.withLock { _saves } }
        set { lock.withLock { _saves = newValue } }
    }
    private var _saves: [SaveInfo] = []
    func saves(_ box: String) async throws -> [SaveInfo] {
        try await record(["app", "saves", box])
        return lock.withLock { _saves }
    }
    func setNetwork(usable: Bool) async throws { try await record(["app", "network", usable ? "up" : "down"]) }
    func woke() async throws -> Bool { try await record(["app", "woke"]); return wokeRestarted }
    func relaunch() async throws { try await record(["app", "relaunch"]) }
    func leaveUnsaved(_ box: String) async throws { try await record(["app", "leave-unsaved", box]) }
    // Tabs: in memory, like the box agent: new tabs go at the end and are
    // shown; watchers get the list at once and on pushTabs.
    private var _tabs = [TabInfo(id: "@0", name: "shell", active: true)]
    private var nextTab = 1
    private var tabWatchers: [AsyncThrowingStream<[TabInfo], Error>.Continuation] = []

    func tabs(_ box: String) async throws -> [TabInfo] {
        try await record(["app", "list-tab", box])
        return lock.withLock { _tabs }
    }
    func newTab(_ box: String, name: String) async throws -> TabInfo {
        try await record(["app", "new-tab", box, name])
        return lock.withLock {
            let t = TabInfo(id: "@\(nextTab)", name: name, active: true)
            nextTab += 1
            for i in _tabs.indices { _tabs[i].active = false }
            _tabs.append(t)
            return t
        }
    }
    func closeTab(_ box: String, id: String) async throws {
        try await record(["app", "close-tab", box, id])
        lock.withLock { _tabs = _tabs.filter { $0.id != id } }
    }
    func renameTab(_ box: String, id: String, name: String) async throws {
        try await record(["app", "rename-tab", box, id, name])
        lock.withLock { if let i = _tabs.firstIndex(where: { $0.id == id }) { _tabs[i].name = name } }
    }
    func selectTab(_ box: String, id: String) async throws {
        try await record(["app", "select-tab", box, id])
        lock.withLock { for i in _tabs.indices { _tabs[i].active = _tabs[i].id == id } }
    }
    func watchTabs(_ box: String) -> AsyncThrowingStream<[TabInfo], Error> {
        lock.withLock { _calls.append(["app", "watch-tabs", box]) }
        let first = lock.withLock { _tabs }
        return AsyncThrowingStream { continuation in
            continuation.yield(first)
            lock.withLock { tabWatchers.append(continuation) }
        }
    }
    /// Pushes tabs to every tab watcher, as the box agent reports a change.
    func pushTabs(_ ts: [TabInfo]) {
        lock.withLock { _tabs = ts }
        lock.withLock { tabWatchers }.forEach { $0.yield(ts) }
    }
    /// Changes the tabs without telling watchers (something changed between
    /// two updates).
    func setTabs(_ ts: [TabInfo]) { lock.withLock { _tabs = ts } }
    /// Ends every tab watch, as when the box restarts.
    func endTabWatches() {
        let all = lock.withLock { let w = tabWatchers; tabWatchers = []; return w }
        all.forEach { $0.finish() }
    }

    /// What takeSavedAfterQuit gives out (once).
    var savedAfterQuit: [SavedAfterQuit] = []
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit] {
        try await record(["app", "take-saved-after-quit"])
        return lock.withLock { let s = savedAfterQuit; savedAfterQuit = []; return s }
    }

    // MARK: The team (ADR 0022)

    /// One watch of the team: its events, then it ends, or fails with
    /// `failure` (a message; DaemonError.outOfDate for an older portenvd).
    struct TeamScript: Sendable {
        var events: [(TeamEvent, String)]
        var failure: String?
    }
    /// Each watchTeam plays the next script; after the last, it waits.
    var teamScripts: [TeamScript] = []
    /// The decision answerDecision returns, keyed by id.
    var answered: [String: TeamDecision] = [:]
    func watchTeam(_ box: String, cursor: String) -> AsyncThrowingStream<(TeamEvent, String), Error> {
        let script: TeamScript? = lock.withLock {
            _calls.append(["app", "watch-team", box, cursor])
            return teamScripts.isEmpty ? nil : teamScripts.removeFirst()
        }
        return AsyncThrowingStream { continuation in
            guard let script else { return }
            for e in script.events { continuation.yield(e) }
            if let f = script.failure { continuation.finish(throwing: DaemonError(f)) } else { continuation.finish() }
        }
    }
    func answerDecision(_ box: String, id: String, approve: Bool, note: String) async throws -> TeamDecision {
        try await record(["app", "answer-decision", box, id, approve ? "approve" : "decline", note])
        guard let d = lock.withLock({ answered[id] }) else { throw DaemonError("No decision \(id).") }
        return d
    }
    func chooseDecision(_ box: String, id: String, choice: Int, label: String) async throws -> TeamDecision {
        try await record(["app", "choose-decision", box, id, String(choice), label])
        guard let d = lock.withLock({ answered[id] }) else { throw DaemonError("No decision \(id).") }
        return d
    }
    func sendMessage(_ box: String, to agent: String, thread: String, text: String) async throws -> TeamMessage {
        try await record(["app", "send-message", box, agent, text])
        return TeamMessage(id: "m-\(lock.withLock { _calls.count })", from: TeamText.owner, to: agent, threadID: thread, text: text)
    }
    func startAgent(_ box: String, id: String) async throws -> TeamAgent {
        try await record(["app", "start-agent", box, id])
        return TeamAgent(id: id, name: id, status: .starting)
    }
    func stopAgent(_ box: String, id: String) async throws -> TeamAgent {
        try await record(["app", "stop-agent", box, id])
        return TeamAgent(id: id, name: id, status: .stopped)
    }
    /// Each agent's file as getAgentFile returns it.
    var agentFiles: [String: AgentFileContent] = [:]
    func getAgentFile(_ box: String, id: String) async throws -> AgentFileContent {
        try await record(["app", "get-agent-file", box, id])
        guard let f = lock.withLock({ agentFiles[id] }) else { throw DaemonError("this workspace has no agent \"\(id)\"") }
        return f
    }
    func approveAgentFile(_ box: String, id: String, sha256: String) async throws -> TeamAgent {
        try await record(["app", "approve-agent-file", box, id, sha256])
        return TeamAgent(id: id, name: id, status: .stopped, fileSHA256: sha256, approved: true)
    }
    /// What ApplyTeamPlan answers (ADR 0025); nothing kept by default.
    var applied = TeamPlanApplied(applied: false)
    /// Answers in turn before `applied` (a needed sign-in, a failed clone).
    var appliedFirst: [TeamPlanApplied] = []
    func applyTeamPlan(_ box: String, githubToken: String?) async throws -> TeamPlanApplied {
        // The sign-in is recorded only as whether one was given.
        try await record(["app", "apply-team-plan", box, githubToken == nil ? "no-token" : "token"])
        return lock.withLock { appliedFirst.isEmpty ? applied : appliedFirst.removeFirst() }
    }
    /// Settings › Team › Agents allowed (R-0347).
    var allowed = 4
    func agentsAllowed() async throws -> Int {
        try await record(["app", "agents-allowed"])
        return lock.withLock { allowed }
    }
    func setAgentsAllowed(_ n: Int) async throws -> Int {
        try await record(["app", "set-agents-allowed", String(n)])
        lock.withLock { allowed = n }
        return n
    }
}
