// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The Help menu (R-0088, R-0097): Send Feedback… opens a new message in the
/// person's mail app (Feedback); Copy Feedback Address copies the address
/// for webmail. They replace the standard help item until there is a help
/// book. With no mail app, an alert gives the address with a button that
/// copies it.
struct FeedbackCommands: Commands {
    var body: some Commands {
        CommandGroup(replacing: .help) {
            Button(Feedback.menuItem) { SendFeedback.open() }
                .help(Feedback.menuItemHelp)
            Button(Feedback.copyMenuItem) { SendFeedback.copyAddress() }
        }
    }
}

@MainActor
enum SendFeedback {
    static func open() {
        let url = Feedback.mailto(.current())
        guard NSWorkspace.shared.urlForApplication(toOpen: url) != nil else {
            showAddress()
            return
        }
        NSWorkspace.shared.open(url, configuration: NSWorkspace.OpenConfiguration()) { _, error in
            guard error != nil else { return }
            Task { @MainActor in showAddress() }
        }
    }

    /// The address, and a button that copies it (only the address).
    static func showAddress() {
        let alert = NSAlert()
        alert.messageText = Feedback.noMailApp
        alert.informativeText = Feedback.noMailAppDetail
        alert.addButton(withTitle: Feedback.copyAddress)
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            copyAddress()
        }
    }

    /// Only the address.
    static func copyAddress() {
        let board = NSPasteboard.general
        board.clearContents()
        board.setString(Feedback.address, forType: .string)
    }
}
