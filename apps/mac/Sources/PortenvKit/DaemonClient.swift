// SPDX-License-Identifier: Apache-2.0

import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import PortenvProto
import SwiftProtobuf

/// portenvd's gRPC API on its Unix socket (gRPC Swift 2, ADR 0014). The
/// socket is 0600 in a directory only this user can read, and portenvd
/// checks every caller's uid. The connection is re-established on its own
/// when portenvd restarts.
public final class DaemonClient: DaemonAPI, Sendable {
    private typealias Transport = HTTP2ClientTransport.Posix
    private let grpc: GRPCClient<Transport>
    private let api: Portenv_Daemon_V1_DaemonService.Client<Transport>

    public init(socket: URL = Binaries.socket) throws {
        let transport = try Transport(target: .unixDomainSocket(path: socket.path), transportSecurity: .plaintext)
        let client = GRPCClient(transport: transport)
        grpc = client
        api = Portenv_Daemon_V1_DaemonService.Client(wrapping: client)
        Task.detached { try? await client.runConnections() }
    }

    deinit { grpc.beginGracefulShutdown() }

    /// What the app does when a call hits a portenvd that doesn't know it
    /// (R-0198): bring up the current one (ServiceUpdate); nil in tests.
    public static let outOfDate = OutOfDateHook()

    /// Runs one call, turning gRPC's errors into a DaemonError with
    /// portenvd's own plain message. A call an older portenvd doesn't know
    /// brings up the current one and is tried once more.
    private func call<T: Sendable>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await callOnce(body)
        } catch let e as DaemonError where e.message == DaemonError.outOfDate {
            guard let fix = Self.outOfDate.fix else { throw e }
            switch await fix() {
            case .restarted: return try await callOnce(body)
            case .current, .refused: throw DaemonError(ServiceUpdate.reinstall)
            }
        }
    }

    private func callOnce<T: Sendable>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let e as RPCError {
            if e.metadata[stringValues: "portenv-refusal"].contains("open-elsewhere") {
                throw DaemonError(e.message, openElsewhere: true)
            }
            throw DaemonError.fromRPC(message: DaemonErrorText.plain(code: e.code, message: e.message), unimplemented: e.code == .unimplemented,
                                      unavailable: e.code == .unavailable || DaemonErrorText.isTransport(e.message))
        }
    }

    private static func named<M: SwiftProtobuf.Message>(_ box: String, _: M.Type) -> M where M: NamedRequest {
        var m = M()
        m.name = box
        return m
    }

    public func ping() async -> Bool {
        var options = CallOptions.defaults
        options.timeout = .seconds(2)
        return (try? await api.getVersion(.init(), options: options)) != nil
    }

    public func engineRunning() async -> Bool? {
        var options = CallOptions.defaults
        options.timeout = .seconds(3)
        return try? await api.getEngine(.init(), options: options).running
    }

    public func boxes() async throws -> [String] {
        // A box CreateBox hasn't finished is never opened (ADR 0019).
        try await call { try await api.listBoxes(.init()).boxes.filter { !$0.creating }.map(\.box.name) }
    }

    public func servers() async throws -> [String] {
        try await call { try await api.listServers(.init()).servers }
    }

    public func open(_ box: String) async throws { try await open(box, takeOver: false) }

    public func open(_ box: String, takeOver: Bool) async throws {
        var req = Self.named(box, Portenv_Daemon_V1_OpenBoxRequest.self)
        req.takeOver = takeOver
        _ = try await call { try await api.openBox(req) }
    }

    public func connectThere(_ box: String) async throws {
        var req = Self.named(box, Portenv_Daemon_V1_OpenBoxRequest.self)
        req.connectThere = true
        _ = try await call { try await api.openBox(req) }
    }

    public func placement(_ box: String, probe: Bool) async throws -> Placement {
        var req = Portenv_Daemon_V1_GetPlacementRequest()
        req.name = box
        req.probe = probe
        let r = try await call { try await api.getPlacement(req) }
        let holder: Placement.Holder = switch r.holder {
        case .knownServer: .knownServer(id: r.serverID, name: r.serverName)
        case .otherMachine: .otherMachine(id: r.machine)
        default: .none
        }
        let answers: Placement.Answers? = switch r.answers {
        case .yes: .yes
        case .no: .no
        case .cannotAsk: .cannotAsk
        default: nil
        }
        return Placement(holder: holder, newestSaveAt: r.hasNewestSaveAt ? r.newestSaveAt.date : nil, answers: answers)
    }

    public func close(_ box: String) async throws {
        _ = try await call { try await api.closeBox(Self.named(box, Portenv_Daemon_V1_CloseBoxRequest.self)) }
    }

    public func makeSavePoint(_ box: String) async throws {
        _ = try await call { try await api.makeSavePoint(Self.named(box, Portenv_Daemon_V1_MakeSavePointRequest.self)) }
    }

    public func revert(_ box: String) async throws -> Date? {
        let r = try await call { try await api.revertToLastSavePoint(Self.named(box, Portenv_Daemon_V1_RevertToLastSavePointRequest.self)) }
        guard r.hasRestored, r.restored.hasTime else { return nil }
        return r.restored.time.date
    }

    public func move(_ box: String, to target: String) async throws {
        var req = Portenv_Daemon_V1_MoveBoxRequest()
        req.name = box
        req.target = target
        _ = try await call { try await api.moveBox(req) }
    }

    public func check(_ box: String) async throws -> Bool {
        try await call { try await api.checkBox(Self.named(box, Portenv_Daemon_V1_CheckBoxRequest.self)).agentAvailable }
    }

    public func restart(_ box: String) async throws {
        _ = try await call { try await api.restartBox(Self.named(box, Portenv_Daemon_V1_RestartBoxRequest.self)) }
    }

    public func state(_ box: String) async throws -> BoxState {
        let r = try await call { try await api.getBoxState(Self.named(box, Portenv_Daemon_V1_GetBoxStateRequest.self)) }
        guard let s = BoxState(r) else { throw DaemonError("portenvd sent a state this app doesn't know") }
        return s
    }

    public func watch(_ box: String) -> AsyncThrowingStream<BoxState, Error> {
        let api = self.api
        let req = Self.named(box, Portenv_Daemon_V1_WatchBoxStateRequest.self)
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await api.watchBoxState(req) { response in
                        for try await message in response.messages {
                            if let s = BoxState(message.state) { continuation.yield(s) }
                        }
                    }
                    continuation.finish()
                } catch let e as RPCError {
                    continuation.finish(throwing: DaemonError.fromRPC(message: e.message, unimplemented: e.code == .unimplemented, unavailable: e.code == .unavailable))
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func keyIDs(_ box: String) async throws -> [String] {
        try await call { try await api.getKeyIDs(Self.named(box, Portenv_Daemon_V1_GetKeyIDsRequest.self)).keyIds }
    }

    public func provideKeys(_ box: String, _ keys: [String: Data]) async throws {
        var req = Portenv_Daemon_V1_ProvideKeysRequest()
        req.name = box
        req.keys = keys
        _ = try await call { try await api.provideKeys(req) }
    }

    public func retryPackages(_ box: String) async throws {
        _ = try await call { try await api.retryPackages(Self.named(box, Portenv_Daemon_V1_RetryPackagesRequest.self)) }
    }

    public func saves(_ box: String) async throws -> [SaveInfo] {
        try await call {
            try await api.listSaves(Self.named(box, Portenv_Daemon_V1_ListSavesRequest.self)).saves.map {
                SaveInfo(id: $0.id, time: $0.time.date, kind: $0.kind, machine: $0.machine)
            }
        }
    }

    public func setNetwork(usable: Bool) async throws {
        var req = Portenv_Daemon_V1_SetNetworkPathRequest()
        req.path = usable ? .satisfied : .unsatisfied
        // portenvd trusts a "down" only while this app runs, and for 30 s.
        req.reporterPid = ProcessInfo.processInfo.processIdentifier
        _ = try await call { try await api.setNetworkPath(req) }
    }

    public func woke() async throws -> Bool {
        try await call { try await api.woke(.init()).boxes.contains { $0.restarted } }
    }

    public func relaunch() async throws {
        _ = try await call { try await api.relaunch(.init()) }
    }

    public func takeSavedAfterQuit() async throws -> [SavedAfterQuit] {
        try await call { try await api.takeSavedAfterQuit(.init()).saved.map { SavedAfterQuit(box: $0.name, savedAt: $0.savedAt.date) } }
    }

    public func leaveUnsaved(_ box: String) async throws {
        _ = try await call { try await api.leaveUnsaved(Self.named(box, Portenv_Daemon_V1_LeaveUnsavedRequest.self)) }
    }

    public func tabs(_ box: String) async throws -> [TabInfo] {
        var req = Portenv_Daemon_V1_ListTabsRequest()
        req.box = box
        req.viewer = TabViewer.app
        return try await call { try await api.listTabs(req).tabs.map(TabInfo.init) }
    }

    public func newTab(_ box: String, name: String) async throws -> TabInfo {
        var req = Portenv_Daemon_V1_NewTabRequest()
        req.box = box
        req.viewer = TabViewer.app
        req.name = name
        return try await call { TabInfo(try await api.newTab(req).tab) }
    }

    public func closeTab(_ box: String, id: String) async throws {
        var req = Portenv_Daemon_V1_CloseTabRequest()
        req.box = box
        req.viewer = TabViewer.app
        req.id = id
        _ = try await call { try await api.closeTab(req) }
    }

    public func renameTab(_ box: String, id: String, name: String) async throws {
        var req = Portenv_Daemon_V1_RenameTabRequest()
        req.box = box
        req.viewer = TabViewer.app
        req.id = id
        req.name = name
        _ = try await call { try await api.renameTab(req) }
    }

    public func selectTab(_ box: String, id: String) async throws {
        var req = Portenv_Daemon_V1_SelectTabRequest()
        req.box = box
        req.viewer = TabViewer.app
        req.id = id
        _ = try await call { try await api.selectTab(req) }
    }

    public func watchTabs(_ box: String) -> AsyncThrowingStream<[TabInfo], Error> {
        let api = self.api
        let req = Portenv_Daemon_V1_WatchTabsRequest.with { $0.box = box; $0.viewer = TabViewer.app }
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await api.watchTabs(req) { response in
                        for try await message in response.messages {
                            continuation.yield(message.tabs.map(TabInfo.init))
                        }
                    }
                    continuation.finish()
                } catch let e as RPCError {
                    continuation.finish(throwing: DaemonError.fromRPC(message: e.message, unimplemented: e.code == .unimplemented, unavailable: e.code == .unavailable))
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}

extension DaemonClient: BoxCreating, RecoveryChecking {
    public func listings() async throws -> [BoxListing] {
        try await call {
            try await api.listBoxes(.init()).boxes.map { BoxListing(name: $0.box.name, id: $0.box.id, creating: $0.creating, hasRecoveryKey: $0.carriesRecoveryKey) }
        }
    }

    public func createBox(_ r: CreateRequest) async throws -> Bool {
        var req = Portenv_Daemon_V1_CreateBoxRequest()
        req.name = r.name
        req.boxID = r.boxID
        req.storage = r.storage
        req.keys = r.keys
        req.recoveryPassword = r.recoveryPassword
        return try await creating { try await api.createBox(req).alreadyCreated }
    }

    public func checkRecoveryKey(_ passwords: [String: Data]) async throws -> RecoveryCheckResult {
        var req = Portenv_Daemon_V1_CheckRecoveryKeyRequest()
        req.recoveryPasswords = passwords
        let r = try await creating { try await api.checkRecoveryKey(req) }
        return RecoveryCheckResult(checked: Int(r.checked), opened: r.opened)
    }

    public func discardCreation(boxID: String) async throws {
        _ = try await creating { try await api.discardCreation(.with { $0.boxID = boxID }) }
    }

    /// CreateBox's and DiscardCreation's errors, by what the app does.
    private func creating<T: Sendable>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let e as RPCError {
            // The app only makes this-Mac boxes, which every portenvd with
            // CreateBox makes: Unimplemented is an older portenvd.
            let message = e.code == .unimplemented ? DaemonError.outOfDate : DaemonErrorText.plain(code: e.code, message: e.message)
            throw CreateBoxError(kind: CreateBoxError.Kind(grpcCode: Self.grpcCode(e.code)), message: message)
        } catch {
            throw CreateBoxError(kind: .daemonGone, message: DaemonErrorText.starting)
        }
    }

    static func grpcCode(_ c: RPCError.Code) -> Int {
        switch c {
        case .invalidArgument: 3
        case .deadlineExceeded: 4
        case .alreadyExists: 6
        case .failedPrecondition: 9
        case .unimplemented: 12
        case .unavailable: 14
        default: 13
        }
    }
}

extension TabInfo {
    init(_ t: Portenv_Types_V1_Tab) {
        self.init(id: t.id, name: t.name, active: t.active, program: t.program,
                  command: t.command, runningSince: t.hasRunningSince ? t.runningSince.date : nil,
                  agentID: t.agentID, paneIDs: t.paneIds)
    }
}

/// Requests that carry only the box's name.
protocol NamedRequest { var name: String { get set } }
extension Portenv_Daemon_V1_OpenBoxRequest: NamedRequest {}
extension Portenv_Daemon_V1_ListSavesRequest: NamedRequest {}
extension Portenv_Daemon_V1_CloseBoxRequest: NamedRequest {}
extension Portenv_Daemon_V1_MakeSavePointRequest: NamedRequest {}
extension Portenv_Daemon_V1_RevertToLastSavePointRequest: NamedRequest {}
extension Portenv_Daemon_V1_CheckBoxRequest: NamedRequest {}
extension Portenv_Daemon_V1_RestartBoxRequest: NamedRequest {}
extension Portenv_Daemon_V1_GetBoxStateRequest: NamedRequest {}
extension Portenv_Daemon_V1_WatchBoxStateRequest: NamedRequest {}
extension Portenv_Daemon_V1_GetKeyIDsRequest: NamedRequest {}
extension Portenv_Daemon_V1_RetryPackagesRequest: NamedRequest {}
extension Portenv_Daemon_V1_LeaveUnsavedRequest: NamedRequest {}

extension BoxState {
    /// The state as portenvd sends it; nil for a state this app doesn't know.
    init?(_ r: Portenv_Daemon_V1_GetBoxStateResponse) {
        let save: Save
        switch r.state {
        case .notSavedYet: save = .notSavedYet
        case .saving: save = .saving
        case .saved: save = .saved
        case .offline: save = .offline
        case .agentUnavailable: save = .agentUnavailable
        case .closed: save = .closed
        case .retrying: save = .retrying
        case .notSaved: save = .notSaved
        case .quitUnsaved: save = .quitUnsaved
        default: return nil
        }
        self.init(save: save, savedAt: r.hasSavedAt ? r.savedAt.date : nil, location: r.location.isEmpty ? nil : r.location)
        failedPackages = r.failedPackages
        interrupted = r.interrupted
        storagePlace = r.storagePlace.isEmpty ? nil : r.storagePlace
        openedAt = r.hasOpenedAt ? r.openedAt.date : nil
        restartedForSecurityUpdate = r.restartedForSecurityUpdate
        if r.hasImagePull { imagePull = ImagePull(done: r.imagePull.doneBytes, total: r.imagePull.totalBytes) }
    }
}

extension DaemonClient: ServerKeys {
    public func serverKeys() async throws -> [ServerKeyInfo] {
        try await call { try await api.listServers(.init()).infos.map { Self.info($0) } }
    }

    public func scanServerKey(_ id: String) async throws -> ServerKeyInfo {
        let r = try await call { try await api.scanServerKey(.with { $0.serverID = id }) }
        return Self.info(r.info, keyType: r.keyType)
    }

    public func trustServerKey(_ id: String, fingerprint: String) async throws -> ServerKeyInfo {
        let r = try await call { try await api.trustServerKey(.with { $0.serverID = id; $0.fingerprint = fingerprint }) }
        return Self.info(r.info)
    }

    private static func info(_ i: Portenv_Daemon_V1_ServerInfo, keyType: String = "") -> ServerKeyInfo {
        let state: ServerKeyInfo.State = switch i.state {
        case .confirmed: .confirmed
        case .changed: .changed
        default: .unconfirmed
        }
        return ServerKeyInfo(id: i.id, name: i.name, state: state, fingerprint: i.fingerprint, fromKnownHosts: i.fromKnownHosts, keyType: keyType,
                             wasChanged: i.wasChanged, madeKey: i.madeKey)
    }
}

extension DaemonClient: ServerKeyHandover {
    public func provideServerKey(_ id: String, seed: Data) async throws {
        _ = try await call { try await api.provideServerKey(.with { $0.serverID = id; $0.key = seed }) }
    }
}

/// Holds the app's out-of-date fix (DaemonClient.outOfDate).
public final class OutOfDateHook: @unchecked Sendable {
    private let lock = NSLock()
    private var _fix: (@Sendable () async -> ServiceUpdate.Outcome)?
    public var fix: (@Sendable () async -> ServiceUpdate.Outcome)? {
        get { lock.withLock { _fix } }
        set { lock.withLock { _fix = newValue } }
    }
}

/// The running portenvd, for ServiceUpdate: its build ID (GetVersion) and
/// the executable behind its socket.
public struct DaemonServiceBuild: ServiceBuild {
    let socket: URL
    let client: DaemonClient
    /// The client reconnects by itself when portenvd restarts.
    public init(client: DaemonClient, socket: URL = Binaries.socket) {
        self.client = client
        self.socket = socket
    }

    public func running() async -> RunningService? {
        guard let id = try? await client.buildID() else { return nil }
        return RunningService(buildID: id, executable: DaemonPeer.executable(socket: socket))
    }
}

extension DaemonClient {
    /// portenvd's build ID; empty for one older than build IDs.
    public func buildID() async throws -> String {
        var options = CallOptions.defaults
        options.timeout = .seconds(2)
        return try await callOnce { try await api.getVersion(.init(), options: options).build.buildID }
    }
}

// MARK: The team (ADR 0022)

extension DaemonClient {
    public func watchTeam(_ box: String, cursor: String) -> AsyncThrowingStream<(TeamEvent, String), Error> {
        let api = self.api
        let req = Portenv_Daemon_V1_WatchTeamRequest.with { $0.name = box; $0.cursor = cursor }
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await api.watchTeam(req) { response in
                        for try await message in response.messages {
                            if let event = TeamEvent(message) { continuation.yield((event, message.cursor)) }
                        }
                    }
                    continuation.finish()
                } catch let e as RPCError {
                    continuation.finish(throwing: DaemonError.fromRPC(message: e.message, unimplemented: e.code == .unimplemented, unavailable: e.code == .unavailable))
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func answerDecision(_ box: String, id: String, approve: Bool, note: String) async throws -> TeamDecision {
        let req = Portenv_Daemon_V1_AnswerDecisionRequest.with { $0.name = box; $0.decisionID = id; $0.approve = approve; $0.note = note }
        return try await call { TeamDecision(try await api.answerDecision(req).decision) }
    }

    public func sendMessage(_ box: String, to agent: String, thread: String, text: String) async throws -> TeamMessage {
        let req = Portenv_Daemon_V1_SendMessageRequest.with { $0.name = box; $0.to = agent; $0.threadID = thread; $0.text = text }
        return try await call { TeamMessage(try await api.sendMessage(req).message) }
    }

    public func startAgent(_ box: String, id: String) async throws -> TeamAgent {
        let req = Portenv_Daemon_V1_StartAgentRequest.with { $0.name = box; $0.agentID = id }
        return try await call { TeamAgent(try await api.startAgent(req).agent) }
    }

    public func stopAgent(_ box: String, id: String) async throws -> TeamAgent {
        let req = Portenv_Daemon_V1_StopAgentRequest.with { $0.name = box; $0.agentID = id }
        return try await call { TeamAgent(try await api.stopAgent(req).agent) }
    }

    public func getAgentFile(_ box: String, id: String) async throws -> AgentFileContent {
        let req = Portenv_Daemon_V1_GetAgentFileRequest.with { $0.name = box; $0.agentID = id }
        return try await call {
            let r = try await api.getAgentFile(req)
            return AgentFileContent(content: r.content, sha256: r.fileSha256, approvedContent: r.approvedContent, runner: r.runner)
        }
    }

    public func approveAgentFile(_ box: String, id: String, sha256: String) async throws -> TeamAgent {
        let req = Portenv_Daemon_V1_ApproveAgentFileRequest.with { $0.name = box; $0.agentID = id; $0.fileSha256 = sha256 }
        return try await call { TeamAgent(try await api.approveAgentFile(req).agent) }
    }
}

extension TeamEvent {
    init?(_ r: Portenv_Daemon_V1_WatchTeamResponse) {
        switch r.event {
        case .agent(let a)?: self = .agent(TeamAgent(a))
        case .message(let m)?: self = .message(TeamMessage(m))
        case .decision(let d)?: self = .decision(TeamDecision(d))
        case .removedAgentID(let id)?: self = .removed(id)
        case nil: return nil
        }
    }
}

extension TeamAgent {
    init(_ a: Portenv_Daemon_V1_AgentInfo) {
        let status: AgentStatus = switch a.state {
        case .starting: .starting
        case .running: .running
        case .idle: .idle
        case .needsYou: .needsYou
        case .invalid: .invalid
        case .connected: .connected
        case .disconnected: .disconnected
        case .stopped, .unspecified, .UNRECOGNIZED: .stopped
        }
        self.init(id: a.id, name: a.name, role: a.role, avatar: a.avatar, remote: a.remote, via: a.via, status: status,
                  problem: a.problem, fileSHA256: a.fileSha256, approved: a.approved, tabIDs: a.tabIds, lastLine: a.lastLine,
                  pendingDecisions: Int(a.pendingDecisions), homeBytes: a.homeBytes, homeQuotaBytes: a.homeQuotaBytes,
                  runner: a.runner, grant: TeamGrant(a.grant),
                  workStatus: a.hasStatus ? AgentWorkStatus(a.status) : nil,
                  routines: a.routines.map { AgentRoutine(every: TimeInterval($0.everyMinutes) * 60, note: $0.note, paused: $0.paused) },
                  routinesOff: a.routinesOff)
    }
}

extension AgentWorkStatus {
    init(_ s: Portenv_Types_V1_AgentStatus) {
        // Both 0 when not given; the kit drops any progress that isn't N of M.
        self.init(text: s.text, progress: (Int(s.progressDone), Int(s.progressTotal)), blocked: s.blocked,
                  setAt: s.hasSetAt ? s.setAt.date : nil)
    }
}

extension TeamGrant {
    init(_ g: Portenv_Types_V1_AgentGrant) {
        func mode(_ m: Portenv_Types_V1_GrantMode) -> GrantMode {
            switch m {
            case .allow: .allow
            case .never: .never
            case .ask, .unspecified, .UNRECOGNIZED: .ask
            }
        }
        self.init(panes: mode(g.panes), maxPanes: Int(g.maxPanes), message: g.message, askOwner: g.askOwner,
                  // Packages are never allowed without asking (§6).
                  packages: mode(g.packages) == .never ? .never : .ask)
    }
}

extension TeamMessage {
    init(_ m: Portenv_Types_V1_BoardMessage) {
        let kind: Kind = switch m.type {
        case .question: .question
        case .notice: .notice
        // These show as notices until the sidebar gives them their own
        // kinds (lane D).
        case .statusRequest, .routine, .handoff: .notice
        case .answer, .unspecified, .UNRECOGNIZED: .answer
        }
        self.init(id: m.id, from: m.from, to: m.to, threadID: m.threadID, kind: kind, text: m.text,
                  createdAt: m.hasCreatedAt ? m.createdAt.date : nil)
    }
}

extension TeamDecision {
    init(_ d: Portenv_Types_V1_Decision) {
        let kind: Kind = switch d.kind {
        case .pane: .pane
        case .agentFileChange: .agentFileChange
        case .shareLogin: .shareLogin
        case .package: .package
        case .teamStart: .teamStart
        case .deleteAgentFiles: .deleteAgentFiles
        case .askOwner, .unspecified, .UNRECOGNIZED: .askOwner
        }
        let state: State = switch d.state {
        case .approved: .approved
        case .declined: .declined
        case .expired: .expired
        // An unknown state is never answerable (an expired decision never
        // turns into an approval).
        case .unspecified, .UNRECOGNIZED: .expired
        case .pending: .pending
        }
        self.init(id: d.id, from: d.from, kind: kind, summary: d.summary, detail: d.detail, state: state,
                  answerNote: d.answerNote, createdAt: d.hasCreatedAt ? d.createdAt.date : nil,
                  answeredAt: d.hasAnsweredAt ? d.answeredAt.date : nil, expiresAt: d.hasExpiresAt ? d.expiresAt.date : nil,
                  packages: d.packages.map { Package(name: $0.name, version: $0.version, source: $0.source, dependency: $0.dependency) })
    }
}
