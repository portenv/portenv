// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit

/// The launch alerts for servers' host keys (ADR 0021, R-0180): a server
/// waiting to be confirmed, with its fingerprint (Trust or Not Now), and a
/// key that can't be trusted (OK only).
enum ServerKeyAlert {
    /// Once per launch (ServerKeyCheck), after the window is up.
    @MainActor static let check = ServerKeyCheck(keys: SharedDaemon.client as? ServerKeys ?? NoServerKeys())

    @MainActor static func question(_ q: ServerKeyQuestion) -> NSAlert {
        let alert = NSAlert()
        alert.messageText = q.title
        alert.informativeText = q.detail
        alert.accessoryView = fingerprintView(q)
        alert.addButton(withTitle: ServerKeyQuestion.trust)
        alert.addButton(withTitle: ServerKeyQuestion.notNow)
        // Not Now on Return: Trust is only ever chosen, never a stray key.
        alert.buttons[0].keyEquivalent = ""
        alert.buttons[1].keyEquivalent = "\r"
        return alert
    }

    /// The fingerprint, whole on one line and selectable, with its key type
    /// under it.
    @MainActor private static func fingerprintView(_ q: ServerKeyQuestion) -> NSView {
        let fingerprint = NSTextField(labelWithString: q.fingerprint)
        fingerprint.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        fingerprint.isSelectable = true
        fingerprint.lineBreakMode = .byClipping
        fingerprint.setAccessibilityLabel("Fingerprint \(q.fingerprint)")
        let type = NSTextField(labelWithString: q.keyName)
        type.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        type.textColor = .secondaryLabelColor
        let stack = NSStackView(views: [fingerprint, type])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 2
        stack.frame.size = stack.fittingSize
        return stack
    }

    @MainActor static func notice(_ n: ServerKeyNotice) -> NSAlert {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = n.title
        alert.informativeText = n.detail
        alert.addButton(withTitle: "OK")
        return alert
    }

    @MainActor static func runAtLaunch() async {
        await check.run(
            ask: { q in question(q).runModal() == .alertFirstButtonReturn },
            tell: { n in _ = notice(n).runModal() }
        )
    }
}

/// Stands in when portenvd's client couldn't be made: no servers.
private struct NoServerKeys: ServerKeys {
    func serverKeys() async throws -> [ServerKeyInfo] { [] }
    func scanServerKey(_: String) async throws -> ServerKeyInfo { throw DaemonError(DaemonError.outOfDate) }
    func trustServerKey(_: String, fingerprint _: String) async throws -> ServerKeyInfo { throw DaemonError(DaemonError.outOfDate) }
}
