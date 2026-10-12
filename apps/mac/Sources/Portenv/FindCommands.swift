// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Edit › Find (R-0119): Find… ⌘F, Find Next ⌘G, Find Previous ⇧⌘G and
/// Use Selection for Find ⌘E, sent along the responder chain as
/// performTextFinderAction(_:), which NSTextView (with its find bar) and
/// SwiftTerm's terminal view both handle. The action rides on the sender's
/// tag, as it does for a menu item AppKit builds itself.
struct FindCommands: Commands {
    var body: some Commands {
        CommandGroup(after: .textEditing) {
            Menu(FindMenu.menu) {
                Button(FindMenu.find) { Self.send(.showFindInterface) }
                    .keyboardShortcut("f")
                Button(FindMenu.findNext) { Self.send(.nextMatch) }
                    .keyboardShortcut("g")
                Button(FindMenu.findPrevious) { Self.send(.previousMatch) }
                    .keyboardShortcut("g", modifiers: [.command, .shift])
                Button(FindMenu.useSelection) { Self.send(.setSearchString) }
                    .keyboardShortcut("e")
            }
        }
    }

    @MainActor
    static func send(_ action: NSTextFinder.Action) {
        NSApp.sendAction(#selector(NSResponder.performTextFinderAction(_:)), to: nil, from: FindMenu.sender(action))
    }
}
