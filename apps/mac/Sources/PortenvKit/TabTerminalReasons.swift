// SPDX-License-Identifier: Apache-2.0

import GRPCCore

/// The Disconnected card's lines (ADR 0020; GUIDELINES.md §10).
public enum TabTerminalReasons {
    public static let serviceDown = "Portenv's background service isn't running. It starts again on its own; if this lasts, quit and reopen Portenv."
    public static let helperDown = "The workspace helper isn't answering. Restarting the workspace brings it back."
    public static let tmuxTooOld = "This workspace's tmux is too old for the app's terminal. It needs tmux 3.4 or later."
    public static let closed = "The workspace was closed."
    public static let wrongHelper = "Portenv reached another workspace's helper, so nothing was sent. Restart the workspace."
    public static let noTabTerminals = "The workspace helper can't show tab terminals. Restart the workspace."
    public static let notAllowed = "Portenv couldn't confirm it may open this terminal."
    public static let tooSlow = "The workspace didn't send the tab's screen in time."
    public static let typingLost = "Some typing didn't reach the tab, so the terminal stopped."
    public static let unreadable = "The workspace couldn't read what Portenv sent. Updating Portenv may fix this."
    public static let lost = "The connection to the workspace was lost."

    /// The line for a status from portenvd (or the helper, through it).
    public static func line(code: RPCError.Code, message: String) -> String {
        let m = message.lowercased()
        switch code {
        case .unimplemented:
            return DaemonError.outOfDate
        case .failedPrecondition:
            if m.contains("tmux is too old") { return tmuxTooOld }
            if m.contains("not open on this mac") { return closed }
            if m.contains("didn't say which workspace") || m.contains("another workspace's helper") { return wrongHelper }
            if m.contains("tab terminals") { return noTabTerminals }
            return closed
        case .notFound:
            return closed
        case .unavailable:
            if m.contains("workspace helper is unavailable") { return helperDown }
            if m.contains("weren't delivered") { return typingLost }
            if m.contains("tab terminal") { return helperDown }
            // grpc-swift's own: the socket isn't there or refuses.
            return serviceDown
        case .permissionDenied, .unauthenticated:
            return notAllowed
        case .deadlineExceeded:
            return tooSlow
        case .invalidArgument:
            return unreadable
        default:
            return lost
        }
    }
}
