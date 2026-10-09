// SPDX-License-Identifier: Apache-2.0

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
}
