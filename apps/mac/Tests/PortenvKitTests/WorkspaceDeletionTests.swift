// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// portenvd's delete calls, recording every call and Keychain delete in one
/// order, so a test can say which came first.
final class FakeDeleting: WorkspaceDeleting, @unchecked Sendable {
    private let lock = NSLock()
    var plan = DeletePlan(boxID: "box-0123456789ab", saveCount: 12, savesBytes: 340_000_000)
    /// Each deleteBox answer in turn (the last repeats); an Error is thrown.
    var answers: [Result<DeleteOutcome, Error>] = []
    /// Fails the first deleteBox with portenvd's Keychain line, as when it
    /// can't read the key without a prompt.
    var needsKeys = false
    var keyIDList = ["box-0123456789ab"]
    private(set) var provided: [String: Data] = [:]
    let log: EventLog

    init(log: EventLog) { self.log = log }

    func deletePlan(_ box: String) async throws -> DeletePlan {
        log.add("plan \(box)")
        return plan
    }

    func deleteBox(_ box: String, confirmName: String, savesElsewhereConfirmed: Bool, keysDeleted: Bool) async throws -> DeleteOutcome {
        log.add("delete \(box) typed=\(confirmName) elsewhere=\(savesElsewhereConfirmed) keysDeleted=\(keysDeleted)")
        let next: Result<DeleteOutcome, Error>? = lock.withLock {
            if needsKeys {
                needsKeys = false
                return .failure(DaemonError(KeychainWait.daemonError))
            }
            guard !answers.isEmpty else { return nil }
            return answers.count > 1 ? answers.removeFirst() : answers[0]
        }
        guard let next else { throw DaemonError("no answer") }
        return try next.get()
    }

    func keyIDs(_ box: String) async throws -> [String] { keyIDList }

    func provideKeys(_ box: String, _ keys: [String: Data]) async throws {
        log.add("provide \(keys.keys.sorted())")
        lock.withLock { provided = keys }
    }
}

/// One ordered list of what happened.
final class EventLog: @unchecked Sendable {
    private let lock = NSLock()
    private var _events: [String] = []
    var events: [String] { lock.withLock { _events } }
    func add(_ e: String) { lock.withLock { _events.append(e) } }
}

/// A Keychain store that writes its deletes into the shared log.
final class LoggedSecretStore: SecretStore, @unchecked Sendable {
    let inner = FakeSecretStore()
    let log: EventLog
    init(log: EventLog) { self.log = log }
    func read(account: String) throws -> Data? { try inner.read(account: account) }
    func add(account: String, data: Data, label: String) throws { try inner.add(account: account, data: data, label: label) }
    func set(account: String, data: Data, label: String) throws { try inner.set(account: account, data: data, label: label) }
    func delete(account: String) throws {
        log.add("keychain delete \(account)")
        try inner.delete(account: account)
    }
}

/// Delete Workspace… (ADR 0029): the sheet's confirmation and the app's
/// part of the deletion, the key.
struct WorkspaceDeletionTests {
    let key = Data("k".utf8)

    func deleter(_ log: EventLog) -> (WorkspaceDeleter, FakeDeleting, LoggedSecretStore) {
        let d = FakeDeleting(log: log)
        let keys = LoggedSecretStore(log: log)
        try? keys.inner.add(account: "box-0123456789ab", data: key, label: "k")
        try? keys.inner.add(account: "box-ffffffffffff", data: key, label: "other")
        return (WorkspaceDeleter(daemon: d, keys: keys), d, keys)
    }

    // MARK: The confirmation sheet

    /// Delete stays off until the workspace's name is typed exactly: no
    /// other case, no extra spaces, no prefix.
    @Test func deleteNeedsTheExactName() {
        let c = DeleteConfirmation(name: "acme-api", plan: DeletePlan(boxID: "box-0123456789ab", saveCount: 3, savesBytes: 1))
        for typed in ["", "acme", "Acme-api", "ACME-API", "acme-api ", " acme-api", "acme-api\n", "acme_api"] {
            #expect(!c.canDelete(typed: typed, savesElsewhereConfirmed: false), "typed \(typed.debugDescription)")
        }
        #expect(c.canDelete(typed: "acme-api", savesElsewhereConfirmed: false))
    }

    /// It names exactly what goes, says it can't be undone (R-0358), and
    /// says how to confirm.
    @Test func theSheetSaysWhatGoesAndThatItCantBeUndone() {
        let c = DeleteConfirmation(name: "acme-api", plan: DeletePlan(boxID: "box-0123456789ab", saveCount: 12, savesBytes: 340_000_000,
                                                                      carriesRecoveryKey: true))
        #expect(c.title == "Delete “acme-api”?")
        let text = c.removes.joined(separator: "\n")
        #expect(text.contains("12 saves"))
        #expect(text.contains("340 MB"))
        #expect(text.contains("Keychain"))
        #expect(c.cantBeUndone == "This can’t be undone.")
        #expect(c.typePrompt.contains("acme-api"))
        #expect(c.deleteButton == "Delete Workspace")
        // One save, and none.
        #expect(DeleteConfirmation(name: "a", plan: DeletePlan(boxID: "b", saveCount: 1, savesBytes: 10)).removes[0].contains("1 save "))
        #expect(DeleteConfirmation(name: "a", plan: DeletePlan(boxID: "b", saveCount: 0, savesBytes: 0)).removes.allSatisfy { !$0.contains("0 saves") })
    }

    /// Saves on a server stay there, with the key: the sheet says where, and
    /// Delete needs the second confirmation too.
    @Test func savesElsewhereNeedTheSecondConfirmation() {
        let c = DeleteConfirmation(name: "far-api", plan: DeletePlan(boxID: "b", savesElsewhere: true, savesPlace: "test-server"))
        #expect(c.savesElsewhere?.contains("test-server") == true)
        #expect(c.secondConfirmation?.contains("test-server") == true)
        #expect(!c.removes.joined().contains("Keychain"))
        #expect(!c.canDelete(typed: "far-api", savesElsewhereConfirmed: false))
        #expect(c.canDelete(typed: "far-api", savesElsewhereConfirmed: true))
    }

    /// A workspace that is open, or that portenvd refuses (an agent running,
    /// R-0358), is never deletable from the sheet, whatever is typed; the
    /// sheet says portenvd's reason.
    @Test func aRefusalOrAnOpenWorkspaceBlocksDelete() {
        var plan = DeletePlan(boxID: "b", refusal: "acme-api has agents running. Stop its agents first (stop its team), then delete it.")
        var c = DeleteConfirmation(name: "acme-api", plan: plan)
        #expect(!c.canDelete(typed: "acme-api", savesElsewhereConfirmed: true))
        #expect(c.blocked?.contains("Stop its agents") == true)
        plan = DeletePlan(boxID: "b", openHere: true, refusal: "acme-api is open. Close it first, then delete it.")
        c = DeleteConfirmation(name: "acme-api", plan: plan)
        #expect(!c.canDelete(typed: "acme-api", savesElsewhereConfirmed: true))
        #expect(c.offersClose)
    }

    /// The menus: Delete Workspace… comes last, after a divider, and is
    /// disabled while an agent runs (R-0358) or the window is busy.
    @Test func theMenuOffersDeleteLast() {
        let items = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [])
        #expect(items.suffix(2).map(\.title) == ["—", "Delete Workspace…"])
        #expect(items.last?.action == .deleteWorkspace && items.last?.enabled == true)
        let agents = BoxMenuModel.items(open: true, busy: false, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [],
                                        agentsRunning: true)
        #expect(agents.last?.enabled == false)
        let busy = BoxMenuModel.items(open: true, busy: true, agentUnavailable: false, optionHeld: false, location: "this-mac", servers: [])
        #expect(busy.last?.enabled == false)
        // Closed, it's still offered.
        let closed = BoxMenuModel.items(open: false, busy: false, agentUnavailable: false, optionHeld: false, location: nil, servers: [])
        #expect(closed.last?.title == "Delete Workspace…" && closed.last?.enabled == true)
    }

    // MARK: The key

    /// The key goes only after portenvd says the saves are gone, and only
    /// the keys it names; then portenvd removes its record.
    @Test func theKeyGoesOnlyAfterPortenvdSaysTheSavesAreGone() async throws {
        let log = EventLog()
        let (deleter, d, keys) = deleter(log)
        d.answers = [.success(DeleteOutcome(deleteKeyIDs: ["box-0123456789ab"], step: .key)),
                     .success(DeleteOutcome(finished: true, step: .record))]
        let out = try await deleter.delete("acme-api", typed: "acme-api", savesElsewhereConfirmed: false)
        #expect(out.finished)
        #expect(log.events == [
            "delete acme-api typed=acme-api elsewhere=false keysDeleted=false",
            "keychain delete box-0123456789ab",
            "delete acme-api typed=acme-api elsewhere=false keysDeleted=true",
        ])
        #expect(keys.inner.all["box-0123456789ab"] == nil)
        #expect(keys.inner.all["box-ffffffffffff"] != nil)
    }

    /// portenvd stopped before the saves were gone: no key is touched, and
    /// its line comes back to say where it stopped.
    @Test func aStopBeforeTheSavesLeavesTheKey() async {
        let log = EventLog()
        let (deleter, d, keys) = deleter(log)
        d.answers = [.failure(DaemonError("Deleting acme-api stopped while deleting its saves. What's left of it is still there. Choose Delete Workspace… again to continue."))]
        await #expect(throws: DaemonError.self) {
            _ = try await deleter.delete("acme-api", typed: "acme-api", savesElsewhereConfirmed: false)
        }
        #expect(!log.events.contains { $0.hasPrefix("keychain") })
        #expect(keys.inner.all["box-0123456789ab"] != nil)
    }

    /// Saves elsewhere: portenvd finishes with no key to delete, and none is.
    @Test func savesElsewhereKeepTheKey() async throws {
        let log = EventLog()
        let (deleter, d, keys) = deleter(log)
        d.answers = [.success(DeleteOutcome(finished: true, savesLeftAt: "test-server", step: .record))]
        let out = try await deleter.delete("far-api", typed: "far-api", savesElsewhereConfirmed: true)
        #expect(out.savesLeftAt == "test-server")
        #expect(!log.events.contains { $0.hasPrefix("keychain") })
        #expect(keys.inner.all["box-0123456789ab"] != nil)
    }

    /// The Keychain refusing the delete leaves portenvd's record (it shows
    /// as stopped at the key) and says so; a retry finishes.
    @Test func aKeychainRefusalStopsBeforeTheRecord() async throws {
        let log = EventLog()
        let (deleter, d, keys) = deleter(log)
        keys.inner.failDeletes = ["box-0123456789ab"]
        d.answers = [.success(DeleteOutcome(deleteKeyIDs: ["box-0123456789ab"], step: .key))]
        await #expect(throws: WorkspaceDeleter.KeyNotDeleted.self) {
            _ = try await deleter.delete("acme-api", typed: "acme-api", savesElsewhereConfirmed: false)
        }
        #expect(!log.events.contains { $0.contains("keysDeleted=true") })
    }

    /// portenvd can't read the key without a prompt: the app (in front of
    /// the person) reads it, hands it over, and asks again.
    @Test func theAppReadsTheKeyWhenPortenvdCant() async throws {
        let log = EventLog()
        let (deleter, d, _) = deleter(log)
        d.needsKeys = true
        d.answers = [.success(DeleteOutcome(deleteKeyIDs: ["box-0123456789ab"], step: .key)),
                     .success(DeleteOutcome(finished: true, step: .record))]
        let out = try await deleter.delete("acme-api", typed: "acme-api", savesElsewhereConfirmed: false)
        #expect(out.finished)
        #expect(d.provided["box-0123456789ab"] == key)
        #expect(log.events.first { $0.hasPrefix("provide") } != nil)
    }

    // MARK: The list

    /// After a deletion the workspace leaves the list; if it was the last
    /// one opened, the next launch shows the list rather than opening
    /// another.
    @Test func theListDropsItAndLaunchFallsBackToTheList() {
        #expect(WorkspaceList.afterDeleting("acme-api", from: ["acme-api", "other-api"]) == ["other-api"])
        #expect(LaunchDecision.decide(requested: nil, boxes: ["other-api"], lastBox: "acme-api", firstRunDone: true) == .boxList(notice: nil))
    }

    /// ADR 0030 point 6: a deletion that stopped at the attachment store
    /// says so: portenvd's step 6 is the app's .attachments, never "not
    /// stopped".
    @Test func aDeletionStoppedAtTheAttachmentsIsKnown() {
        #expect(DeleteStep(rawValue: 6) == .attachments)
        #expect(DeleteStep(rawValue: 6) != DeleteStep.none)
    }
}
