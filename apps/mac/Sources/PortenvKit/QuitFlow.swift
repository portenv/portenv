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
/// It uses the CLI directly, off the main actor: while AppKit waits for the
/// quit reply, main-actor work does not run.
public struct QuitFlow: Sendable {
    let cli: CLIRunning
    let box: String
    /// Whether the box is open on this Mac (a box on a server, or closed,
    /// needs nothing here).
    let openHere: Bool

    public init(cli: CLIRunning, box: String, openHere: Bool) {
        self.cli = cli
        self.box = box
        self.openHere = openHere
    }

    /// The alert's text when the box couldn't be saved.
    public static func message(box: String) -> String {
        "\(box) couldn't be saved before quitting. Its work is still on this Mac."
    }

    /// Closes the box, asking on failure. Returns true to quit, false to stay.
    public func run(ask: @Sendable (String) async -> QuitChoice) async -> Bool {
        guard openHere else { return true }
        while true {
            do {
                _ = try await cli.run(["app", "close", box])
                return true
            } catch {
                switch await ask(Self.message(box: box)) {
                case .tryAgain: continue
                case .quitAnyway: return true
                case .cancel: return false
                }
            }
        }
    }
}
