// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Which terminal the main window shows (ADR 0020, decision 12): each tab's
/// own control-mode terminal, or, in a dev lane's build made with
/// `gmake app LANE=X ATTACHED_TERMINAL=1`, the old attached one, until the
/// latency gate passes.
enum AppTerminal {
    #if PORTENV_ATTACHED_TERMINAL
        static let attached = true
        /// Kept in the binary, so check-app-identity.sh can refuse such a
        /// build as a release.
        static let marker = "PORTENV_ATTACHED_TERMINAL_BUILD"
    #else
        static let attached = false
        static let marker = ""
    #endif

    /// Says so at launch in a build that shows the attached terminal.
    static func announce() {
        if attached {
            FileHandle.standardError.write(Data("Portenv: \(marker): the main window shows the attached terminal (a dev lane's build)\n".utf8))
        }
    }
}
