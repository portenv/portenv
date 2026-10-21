// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// Delete Workspace… (ADR 0029). portenvd deletes a workspace in a fixed
/// order (the instance, its files, its saves on this Mac, then its record);
/// the app shows what will go, asks for the name, and deletes the key from
/// the Keychain, which only the app touches, once portenvd says the saves
/// are gone.

/// portenvd's steps (DeleteStep in the daemon API), by its numbers: the
/// attachment store (ADR 0030) runs after home and before saves, but was
/// numbered after record.
public enum DeleteStep: Int, Equatable, Sendable { case none = 0, instance, home, saves, key, record, attachments }

/// What deleting a workspace would remove, as portenvd reads it without a
/// key (GetDeletePlan).
public struct DeletePlan: Equatable, Sendable {
    public var boxID: String
    /// Saves on this Mac and their size on disk.
    public var saveCount: Int
    public var savesBytes: Int64
    /// The saves are on a server or in a bucket: they stay there, and so
    /// does the key.
    public var savesElsewhere: Bool
    public var savesPlace: String
    /// Open here: it's closed first, the usual way.
    public var openHere: Bool
    /// portenvd's reason it can't be deleted now ("" when it can).
    public var refusal: String
    /// A deletion that stopped part-way.
    public var stoppedAt: DeleteStep
    public var carriesRecoveryKey: Bool

    public init(boxID: String, saveCount: Int = 0, savesBytes: Int64 = 0, savesElsewhere: Bool = false, savesPlace: String = "",
                openHere: Bool = false, refusal: String = "", stoppedAt: DeleteStep = .none, carriesRecoveryKey: Bool = false) {
        self.boxID = boxID
        self.saveCount = saveCount
        self.savesBytes = savesBytes
        self.savesElsewhere = savesElsewhere
        self.savesPlace = savesPlace
        self.openHere = openHere
        self.refusal = refusal
        self.stoppedAt = stoppedAt
        self.carriesRecoveryKey = carriesRecoveryKey
    }
}

/// DeleteBox's answer.
public struct DeleteOutcome: Equatable, Sendable {
    /// portenvd's record is gone: the workspace is deleted from this Mac.
    public var finished: Bool
    /// The saves are gone: the Keychain items to delete now. Empty while
    /// any save remains.
    public var deleteKeyIDs: [String]
    /// Saves left where they are ("" when none).
    public var savesLeftAt: String
    public var step: DeleteStep

    public init(finished: Bool = false, deleteKeyIDs: [String] = [], savesLeftAt: String = "", step: DeleteStep = .none) {
        self.finished = finished
        self.deleteKeyIDs = deleteKeyIDs
        self.savesLeftAt = savesLeftAt
        self.step = step
    }
}

/// portenvd's calls for deleting a workspace.
public protocol WorkspaceDeleting: Sendable {
    func deletePlan(_ box: String) async throws -> DeletePlan
    func deleteBox(_ box: String, confirmName: String, savesElsewhereConfirmed: Bool, keysDeleted: Bool) async throws -> DeleteOutcome
    func keyIDs(_ box: String) async throws -> [String]
    func provideKeys(_ box: String, _ keys: [String: Data]) async throws
}

/// The confirmation sheet's words and its one rule: Delete only with the
/// name typed exactly (GUIDELINES §10, R-0358).
public struct DeleteConfirmation: Equatable, Sendable {
    public let name: String
    public let plan: DeletePlan

    public init(name: String, plan: DeletePlan) {
        self.name = name
        self.plan = plan
    }

    public var title: String { "Delete “\(name)”?" }

    /// What goes, one line each.
    public var removes: [String] {
        var lines: [String] = []
        if !plan.savesElsewhere {
            switch plan.saveCount {
            case 0: lines.append("Its saves on this Mac (there are none yet)")
            case 1: lines.append("Its 1 save on this Mac (\(Self.size(plan.savesBytes)))")
            default: lines.append("All \(plan.saveCount) saves on this Mac (\(Self.size(plan.savesBytes)))")
            }
        }
        lines.append("Everything in the workspace on this Mac: its files, programs and settings")
        if !plan.savesElsewhere {
            lines.append("Its key in this Mac’s Keychain")
        }
        return lines
    }

    /// Saves elsewhere stay, with the key (PLAN: no workspace ever has saves
    /// and no key).
    public var savesElsewhere: String? {
        guard plan.savesElsewhere else { return nil }
        return "Its saves on \(plan.savesPlace) aren’t deleted: they stay there, with this Mac’s key for them, so you can restore the workspace later."
    }

    /// The second confirmation, for saves elsewhere.
    public var secondConfirmation: String? {
        plan.savesElsewhere ? "Leave its saves on \(plan.savesPlace)" : nil
    }

    /// Copies Portenv doesn't control (Time Machine) aren't deleted.
    public var backups: String? {
        guard !plan.savesElsewhere else { return nil }
        return plan.carriesRecoveryKey
            ? "Copies of its saves in Time Machine backups aren’t deleted; your recovery key still opens them."
            : "Copies of its saves in Time Machine backups aren’t deleted."
    }

    public var cantBeUndone: String { "This can’t be undone." }
    public var typePrompt: String { "Type \(name) to confirm." }
    public var deleteButton: String { "Delete Workspace" }

    /// portenvd's reason it can't be deleted now, if any.
    public var blocked: String? { plan.refusal.isEmpty ? nil : plan.refusal }
    /// Open here: the sheet offers Close Workspace first.
    public var offersClose: Bool { plan.openHere }

    public func canDelete(typed: String, savesElsewhereConfirmed: Bool) -> Bool {
        typed == name && plan.refusal.isEmpty && !plan.openHere && (!plan.savesElsewhere || savesElsewhereConfirmed)
    }

    /// "340 MB", in macOS's decimal units.
    static func size(_ bytes: Int64) -> String {
        let b = Double(bytes)
        func n(_ v: Double, _ unit: String) -> String {
            v >= 10 || v == v.rounded() ? "\(Int(v.rounded())) \(unit)" : String(format: "%.1f %@", v, unit)
        }
        switch b {
        case ..<1e3: return "\(bytes) bytes"
        case ..<1e6: return n(b / 1e3, "KB")
        case ..<1e9: return n(b / 1e6, "MB")
        default: return n(b / 1e9, "GB")
        }
    }
}

/// The app's part of a deletion: DeleteBox, the key once portenvd says the
/// saves are gone, then DeleteBox again to remove the record.
public struct WorkspaceDeleter: Sendable {
    /// The Keychain refused to delete the key: portenvd's record stays
    /// (stopped at the key) until a retry deletes it.
    public struct KeyNotDeleted: Error, LocalizedError, Equatable {
        public let name: String
        public var errorDescription: String? {
            "Portenv couldn’t remove the key of \(name) from the Keychain. Its files and saves are already deleted. Choose Delete Workspace… again to finish."
        }
    }

    let daemon: WorkspaceDeleting
    let keys: SecretStore
    public let waits: KeychainWaitRelay

    public init(daemon: WorkspaceDeleting, keys: SecretStore = SystemSecretStore.repositoryKeys, waits: KeychainWaitRelay = KeychainWaitRelay()) {
        self.daemon = daemon
        self.keys = keys
        self.waits = waits
    }

    public func delete(_ box: String, typed: String, savesElsewhereConfirmed: Bool) async throws -> DeleteOutcome {
        let out = try await withKeys(box) {
            try await daemon.deleteBox(box, confirmName: typed, savesElsewhereConfirmed: savesElsewhereConfirmed, keysDeleted: false)
        }
        guard !out.deleteKeyIDs.isEmpty else { return out }
        // portenvd says the saves are gone: now, and only now, the key goes.
        let keys = self.keys, ids = out.deleteKeyIDs, waits = self.waits
        do {
            try await onKeychainQueue(waiting: { waits.report($0) }) {
                for id in ids { try keys.delete(account: id) }
            }
        } catch {
            throw KeyNotDeleted(name: box)
        }
        return try await daemon.deleteBox(box, confirmName: typed, savesElsewhereConfirmed: savesElsewhereConfirmed, keysDeleted: true)
    }

    /// portenvd never prompts: when it needs the key to check the lease,
    /// the app (in front of the person) reads it, hands it over, and asks
    /// again, as an open does.
    private func withKeys<T: Sendable>(_ box: String, _ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let e as DaemonError where e.message.contains(KeychainWait.daemonError) {
            let ids = try await daemon.keyIDs(box)
            let keys = self.keys, waits = self.waits
            let read: [String: Data] = try await onKeychainQueue(waiting: { waits.report($0) }) {
                var out: [String: Data] = [:]
                for id in ids {
                    if let k = try keys.read(account: id) { out[id] = k }
                }
                return out
            }
            try await daemon.provideKeys(box, read)
            return try await body()
        }
    }
}

/// The workspace list after a deletion.
public enum WorkspaceList {
    public static func afterDeleting(_ name: String, from boxes: [String]) -> [String] {
        boxes.filter { $0 != name }
    }
}

/// The Delete Workspace sheet's state: the plan, the typed name, the second
/// confirmation, and the deletion's progress.
@MainActor
@Observable
public final class DeleteFlow: Identifiable {
    public nonisolated let id = UUID()
    public let name: String
    public private(set) var confirmation: DeleteConfirmation?
    public var typed = ""
    public var savesElsewhereConfirmed = false
    public private(set) var working = false
    /// What stopped it, in portenvd's or the Keychain's plain words.
    public private(set) var failure: String?
    public private(set) var outcome: DeleteOutcome?

    @ObservationIgnored let daemon: WorkspaceDeleting
    @ObservationIgnored let deleter: WorkspaceDeleter
    /// Closes the workspace the usual way (saved and released).
    @ObservationIgnored let close: (String) async -> Void

    public init(name: String, daemon: WorkspaceDeleting, deleter: WorkspaceDeleter, close: @escaping (String) async -> Void) {
        self.name = name
        self.daemon = daemon
        self.deleter = deleter
        self.close = close
    }

    public func load() async {
        do {
            confirmation = DeleteConfirmation(name: name, plan: try await daemon.deletePlan(name))
        } catch {
            failure = error.localizedDescription
        }
    }

    public var canDelete: Bool {
        !working && confirmation?.canDelete(typed: typed, savesElsewhereConfirmed: savesElsewhereConfirmed) == true
    }

    /// Close Workspace from the sheet, then the plan again.
    public func closeFirst() async {
        working = true
        await close(name)
        working = false
        await load()
    }

    public func delete() async {
        guard canDelete else { return }
        working = true
        failure = nil
        do {
            outcome = try await deleter.delete(name, typed: typed, savesElsewhereConfirmed: savesElsewhereConfirmed)
        } catch {
            failure = error.localizedDescription
            await load()
        }
        working = false
    }

    /// The list's notice once it's deleted.
    public var notice: String? {
        guard let outcome, outcome.finished else { return nil }
        return outcome.savesLeftAt.isEmpty
            ? "Deleted \(name)."
            : "Deleted \(name) from this Mac. Its saves stay on \(outcome.savesLeftAt)."
    }
}
