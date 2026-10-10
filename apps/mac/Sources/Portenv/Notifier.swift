// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import PortenvKit
import UserNotifications

/// Posts Portenv's notifications. Permission is asked the first time
/// something is worth notifying, never at launch; denied, nothing nags (the
/// state line still says everything).
final class Notifier: KeychainWaitNotifying, @unchecked Sendable {
    static let shared = Notifier()

    func keychainWaiting(box: String) async {
        let center = UNUserNotificationCenter.current()
        guard (try? await center.requestAuthorization(options: [.alert])) == true else { return }
        let content = UNMutableNotificationContent()
        content.title = "\(box) · \(KeychainWait.line)"
        content.body = KeychainWait.detail
        try? await center.add(UNNotificationRequest(identifier: "keychain-\(box)", content: content, trigger: nil))
    }

    /// A command finished in a tab: notified when it ran longer than 30 s
    /// and the window isn't in front (§5). One per tab: a newer one
    /// replaces the older.
    func commandFinished(box: String, finished f: CommandWatch.Finished, location: String?) async {
        guard NotifyRules.commandFinished(seconds: f.seconds, windowInFront: await Self.windowInFront()) else { return }
        await post(NotifyText.commandFinished(box: box, tab: f.tab, program: f.program, seconds: f.seconds, location: location),
                   id: "command-\(box)-\(f.tab)")
    }

    /// A move finished: notified when it took longer than 10 s and the
    /// window isn't in front (§5).
    func moveFinished(box: String, to place: String, seconds: Int) async {
        guard NotifyRules.moveFinished(seconds: seconds, windowInFront: await Self.windowInFront()) else { return }
        await post(NotifyText.moveFinished(box: box, to: place, seconds: seconds), id: "move-\(box)")
    }

    /// Asks for permission the first time something is worth notifying
    /// (never at launch); denied, it posts nothing and never asks again.
    private func post(_ c: NotifyText.Content, id: String) async {
        let center = UNUserNotificationCenter.current()
        guard (try? await center.requestAuthorization(options: [.alert])) == true else { return }
        let content = UNMutableNotificationContent()
        content.title = c.title
        content.subtitle = c.subtitle
        content.body = c.body
        try? await center.add(UNNotificationRequest(identifier: id, content: content, trigger: nil))
    }

    @MainActor private static func windowInFront() -> Bool {
        NSApplication.shared.isActive && NSApplication.shared.keyWindow != nil
    }

    func savedAfterQuit(box: String, at: Date) async {
        let center = UNUserNotificationCenter.current()
        guard (try? await center.requestAuthorization(options: [.alert])) == true else { return }
        let content = UNMutableNotificationContent()
        content.title = SavedAfterQuitNote.title(box: box)
        content.body = SavedAfterQuitNote.body(at: at)
        try? await center.add(UNNotificationRequest(identifier: "saved-after-quit-\(box)", content: content, trigger: nil))
    }
}
