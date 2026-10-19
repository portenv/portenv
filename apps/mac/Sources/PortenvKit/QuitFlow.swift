// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the person chose when a box couldn't be saved before quitting.
public enum QuitChoice: Sendable, Equatable {
    case tryAgain
    case quitAnyway
    case cancel
}

/// Quitting never fails silently (PLAN.md 1.4): the box on this Mac is
/// closed (saved and released) first; if that fails, the person is asked
/// whether to try again, quit anyway (the box stays as it is and is saved
/// first thing on the next launch) or cancel.
///
/// It calls portenvd directly, off the main actor: while AppKit waits for
/// the quit reply, main-actor work does not run.
public struct QuitFlow: Sendable {
    let daemon: DaemonAPI
    let box: String
    /// Whether the box is open on this Mac (a box on a server, or closed,
    /// needs nothing here).
    let openHere: Bool

    public init(daemon: DaemonAPI, box: String, openHere: Bool) {
        self.daemon = daemon
        self.box = box
        self.openHere = openHere
    }

    /// The alert's text when the box couldn't be saved.
    public static func message(box: String) -> String {
        "\(box) couldn't be saved before quitting. Its work is still on this Mac."
    }

    /// Quitting to relaunch for an update (ADR 0014): portenvd stops
    /// without closing the box, so it and its programs keep running and the
    /// next portenvd takes it over. Never asks, never blocks the update.
    public func relaunch() async -> Bool {
        try? await daemon.relaunch()
        return true
    }

    /// Closes the box, asking on failure. Returns true to quit, false to stay.
    /// `tell` says, with OK only, why a close was refused (a Move To in
    /// progress, R-0249).
    public func run(ask: @Sendable (String) async -> QuitChoice, tell: @Sendable (String) async -> Void = { _ in }) async -> Bool {
        await run(message: Self.message(box: box), ask: ask, tell: tell)
    }

    /// The same close, with the alert's words for what's happening
    /// (quitting, or closing the window: WindowClose).
    func run(message: String, ask: @Sendable (String) async -> QuitChoice, tell: @Sendable (String) async -> Void = { _ in }) async -> Bool {
        guard openHere else { return true }
        while true {
            do {
                try await daemon.close(box)
                return true
            } catch let e as DaemonError where e.moving {
                // Never Quit Anyway in the middle of a move: stay.
                await tell(e.message)
                return false
            } catch {
                switch await ask(message) {
                case .tryAgain: continue
                case .quitAnyway:
                    // portenvd (run by launchd) outlives the app: it records
                    // the quit marker and lets go of the box unsaved, so the
                    // next open saves it first thing.
                    try? await daemon.leaveUnsaved(box)
                    return true
                case .cancel: return false
                }
            }
        }
    }
}
