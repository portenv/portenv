// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the app asks portenvd (its gRPC API on the socket, 1.1). Every box
/// action goes through here; the terminal is still `portenv attach`.
public protocol DaemonAPI: Sendable {
    /// Whether portenvd answers.
    func ping() async -> Bool
    /// Whether this Mac's box engine answers (Docker Desktop is running);
    /// nil when portenvd doesn't answer (R-0098).
    func engineRunning() async -> Bool?
    /// The boxes on this Mac, by name (ListBoxes).
    func boxes() async throws -> [String]
    /// The servers a box can move to (user@host).
    func servers() async throws -> [String]
    func open(_ box: String) async throws
    /// Opens it here; takeOver: from a place that doesn't answer (rule 4).
    func open(_ box: String, takeOver: Bool) async throws
    /// Connect to it there: on the known server that holds it.
    func connectThere(_ box: String) async throws
    /// Where the workspace is open when it isn't this Mac (GetPlacement):
    /// at once without the probe, then with it (up to 2 s).
    func placement(_ box: String, probe: Bool) async throws -> Placement
    func close(_ box: String) async throws
    func makeSavePoint(_ box: String) async throws
    /// Reverts to the last save point and returns its time.
    func revert(_ box: String) async throws -> Date?
    func move(_ box: String, to target: String) async throws
    /// Whether the box agent answers.
    func check(_ box: String) async throws -> Bool
    func restart(_ box: String) async throws
    func state(_ box: String) async throws -> BoxState
    /// The box's state as it changes: at once, then on every change. Ends
    /// (or throws) when portenvd goes away.
    func watch(_ box: String) -> AsyncThrowingStream<BoxState, Error>
    /// The Keychain items the app reads for the box.
    func keyIDs(_ box: String) async throws -> [String]
    /// Keys the app read from the Keychain, held in portenvd's memory only.
    func provideKeys(_ box: String, _ keys: [String: Data]) async throws
    func retryPackages(_ box: String) async throws
    /// The box's saves (its history), for the inspector. A box open on a
    /// server lists its saves there, not through this portenvd.
    func saves(_ box: String) async throws -> [SaveInfo]
    /// The system's network status; `reporter` is this app's process.
    func setNetwork(usable: Bool) async throws
    /// After sleep: check every open box's agent channel. Returns whether a
    /// box was restarted (its channel was gone).
    func woke() async throws -> Bool
    /// An update relaunch: portenvd stops, leaving boxes running.
    func relaunch() async throws
    /// Quit Anyway: portenvd records the quit marker and lets go of the
    /// box unsaved; the next open saves it first thing.
    func leaveUnsaved(_ box: String) async throws
    /// Boxes saved in the background after Quit Anyway since last asked
    /// (each given out once).
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit]
    /// The box's tabs (its terminal's tmux windows), in order (1.2).
    func tabs(_ box: String) async throws -> [TabInfo]
    /// A new tab at the end, shown in the terminal.
    func newTab(_ box: String, name: String) async throws -> TabInfo
    func closeTab(_ box: String, id: String) async throws
    func renameTab(_ box: String, id: String, name: String) async throws
    func selectTab(_ box: String, id: String) async throws
    /// The tabs at once, then on every change. Ends (or throws) when the box
    /// closes, its agent goes away, or portenvd does.
    func watchTabs(_ box: String) -> AsyncThrowingStream<[TabInfo], Error>
    /// The team (ADR 0022): every agent, then each change, board message
    /// and decision from `cursor` on, each with the cursor after it.
    func watchTeam(_ box: String, cursor: String) -> AsyncThrowingStream<(TeamEvent, String), Error>
    /// Approve or Decline a pending decision; the app alone answers them.
    func answerDecision(_ box: String, id: String, approve: Bool, note: String) async throws -> TeamDecision
    /// Answers a decision with choices (CC-0501) with one of them: its
    /// number, counting from 1, and its label exactly as portenvd sent it,
    /// which portenvd checks against the decision.
    func chooseDecision(_ box: String, id: String, choice: Int, label: String) async throws -> TeamDecision
    /// The owner's message to an agent.
    func sendMessage(_ box: String, to agent: String, thread: String, text: String) async throws -> TeamMessage
    func startAgent(_ box: String, id: String) async throws -> TeamAgent
    func stopAgent(_ box: String, id: String) async throws -> TeamAgent
    /// An agent file as portenvd has it, for Approve.
    func getAgentFile(_ box: String, id: String) async throws -> AgentFileContent
    /// Pins the agent file whose text hashes to `sha256`; only the owner's
    /// app calls it (R-0285).
    func approveAgentFile(_ box: String, id: String, sha256: String) async throws -> TeamAgent
    /// Writes and pins the team New workspace kept for this workspace,
    /// once it runs (ADR 0025); nothing when there's none.
    func applyTeamPlan(_ box: String, githubToken: String?) async throws -> TeamPlanApplied
    /// Pushes from the workspace waiting for the app (R-0391).
    func watchPushes(_ box: String) -> AsyncThrowingStream<PushAsk, Error>
    /// The app's answer: the sign-in (in memory), and yes or no.
    func answerPush(_ box: String, id: String, allow: Bool, githubToken: String?, signedOut: Bool) async throws
    /// The workspace's repository and whether its pushes are allowed.
    func pushPermission(_ box: String) async throws -> (repo: String, allowed: Bool)
    /// The inspector's Stop Allowing Pushes.
    func stopAllowingPushes(_ box: String) async throws
    /// Settings › Team › Agents allowed (R-0347), as portenvd keeps it.
    func agentsAllowed() async throws -> Int
    /// Sets it; portenvd refuses anything outside 1 to 8. Only after the
    /// owner chose it.
    func setAgentsAllowed(_ n: Int) async throws -> Int
    /// An attachment from this Mac's store (ADR 0030): its record and its
    /// bytes. One the store doesn't hold says AttachmentText.notOnThisMac.
    func readAttachment(_ box: String, id: String) async throws -> (TeamAttachment, Data)
    /// Remove from this Mac: the bytes go; the board keeps the record.
    func removeAttachment(_ box: String, id: String) async throws
    /// The owner's files for agents: each agent gets its own copy in its
    /// ~/inbox/owner/; with a pane, the path is offered there.
    func sendAttachments(_ box: String, to agents: [String], thread: String, pane: String, files: [OutgoingAttachment]) async throws -> SentAttachments
}

public extension DaemonAPI {
    // Defaults for the conformers that don't need them (renders, the
    // unreachable stand-in): an older portenvd.
    func placement(_: String, probe _: Bool) async throws -> Placement { throw DaemonError(DaemonError.outOfDate) }
    func open(_ box: String, takeOver: Bool) async throws {
        guard takeOver else { return try await open(box) }
        throw DaemonError(DaemonError.outOfDate)
    }
    func connectThere(_: String) async throws { throw DaemonError(DaemonError.outOfDate) }
    // The team (ADR 0022): a portenvd without it, or a render.
    func watchTeam(_: String, cursor _: String) -> AsyncThrowingStream<(TeamEvent, String), Error> {
        AsyncThrowingStream { $0.finish(throwing: DaemonError(DaemonError.outOfDate)) }
    }
    func answerDecision(_: String, id _: String, approve _: Bool, note _: String) async throws -> TeamDecision {
        throw DaemonError(DaemonError.outOfDate)
    }
    func chooseDecision(_: String, id _: String, choice _: Int, label _: String) async throws -> TeamDecision {
        throw DaemonError(DaemonError.outOfDate)
    }
    func sendMessage(_: String, to _: String, thread _: String, text _: String) async throws -> TeamMessage {
        throw DaemonError(DaemonError.outOfDate)
    }
    func startAgent(_: String, id _: String) async throws -> TeamAgent { throw DaemonError(DaemonError.outOfDate) }
    func stopAgent(_: String, id _: String) async throws -> TeamAgent { throw DaemonError(DaemonError.outOfDate) }
    func getAgentFile(_: String, id _: String) async throws -> AgentFileContent { throw DaemonError(DaemonError.outOfDate) }
    func approveAgentFile(_: String, id _: String, sha256 _: String) async throws -> TeamAgent { throw DaemonError(DaemonError.outOfDate) }
    func applyTeamPlan(_: String, githubToken _: String?) async throws -> TeamPlanApplied { throw DaemonError(DaemonError.outOfDate) }
    func watchPushes(_: String) -> AsyncThrowingStream<PushAsk, Error> {
        AsyncThrowingStream { $0.finish(throwing: DaemonError(DaemonError.outOfDate)) }
    }
    func answerPush(_: String, id _: String, allow _: Bool, githubToken _: String?, signedOut _: Bool) async throws {
        throw DaemonError(DaemonError.outOfDate)
    }
    func pushPermission(_: String) async throws -> (repo: String, allowed: Bool) { throw DaemonError(DaemonError.outOfDate) }
    func stopAllowingPushes(_: String) async throws { throw DaemonError(DaemonError.outOfDate) }
    func agentsAllowed() async throws -> Int { throw DaemonError(DaemonError.outOfDate) }
    func setAgentsAllowed(_: Int) async throws -> Int { throw DaemonError(DaemonError.outOfDate) }
    func readAttachment(_: String, id _: String) async throws -> (TeamAttachment, Data) { throw DaemonError(DaemonError.outOfDate) }
    func removeAttachment(_: String, id _: String) async throws { throw DaemonError(DaemonError.outOfDate) }
    func sendAttachments(_: String, to _: [String], thread _: String, pane _: String, files _: [OutgoingAttachment]) async throws -> SentAttachments {
        throw DaemonError(DaemonError.outOfDate)
    }
}

/// A box saved in the background after Quit Anyway.
public struct SavedAfterQuit: Equatable, Sendable {
    public let box: String
    public let savedAt: Date
    public init(box: String, savedAt: Date) {
        self.box = box
        self.savedAt = savedAt
    }
}

/// A failed call to portenvd, with its plain message.
public struct DaemonError: Error, LocalizedError, Equatable {
    public let message: String
    /// portenvd isn't there (not running, or it went away).
    public let unavailable: Bool
    /// OpenBox refused because the workspace is open somewhere else
    /// (portenvd's trailer portenv-refusal: open-elsewhere): the lease
    /// sheet, never an error line.
    public let openElsewhere: Bool
    /// CloseBox refused because a Move To is in progress (portenvd's
    /// trailer portenv-refusal: moving, R-0249): said plainly, with no
    /// Close Anyway or Quit Anyway.
    public var moving = false
    public init(_ message: String, unavailable: Bool = false, openElsewhere: Bool = false) {
        self.message = message
        self.unavailable = unavailable
        self.openElsewhere = openElsewhere
    }
    public var errorDescription: String? { message }

    /// portenvd is older than the app: it doesn't know a call the app makes.
    public static let outOfDate =
        "Portenv's background service is out of date. Quit Portenv and open it again; if this keeps happening, reinstall Portenv."

    /// A gRPC failure as the app shows it: portenvd's own plain message,
    /// except a call portenvd doesn't have (an older portenvd), which says
    /// what to do instead of gRPC's "unknown method" (§10).
    public static func fromRPC(message: String, unimplemented: Bool, unavailable: Bool) -> DaemonError {
        unimplemented ? DaemonError(outOfDate) : DaemonError(message, unavailable: unavailable)
    }
}
