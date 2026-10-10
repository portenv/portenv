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

    /// Runs one call, turning gRPC's errors into a DaemonError with
    /// portenvd's own plain message.
    private func call<T: Sendable>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let e as RPCError {
            throw DaemonError.fromRPC(message: e.message, unimplemented: e.code == .unimplemented, unavailable: e.code == .unavailable)
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

    public func servers() async throws -> [String] {
        try await call { try await api.listServers(.init()).servers }
    }

    public func open(_ box: String) async throws {
        _ = try await call { try await api.openBox(Self.named(box, Portenv_Daemon_V1_OpenBoxRequest.self)) }
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

extension TabInfo {
    init(_ t: Portenv_Types_V1_Tab) {
        self.init(id: t.id, name: t.name, active: t.active, program: t.program)
    }
}

/// Requests that carry only the box's name.
protocol NamedRequest { var name: String { get set } }
extension Portenv_Daemon_V1_OpenBoxRequest: NamedRequest {}
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
    }
}
