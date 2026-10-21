// SPDX-License-Identifier: Apache-2.0

import AppKit
import Foundation
import os
import PortenvKit
import UserNotifications

/// Notifications when an agent needs you, and the Dock badge (GUIDELINES
/// §5, basics audit rows 13 and 14, R-0384). Each open workspace's session
/// reports its team and tabs to `center`; what to post, take back and
/// count is decided there (PortenvKit, tested). A click, or Show, brings
/// the workspace forward with the question selected. Nothing here answers
/// a decision: they're answered only in the app (§6.2).
@MainActor
final class NeedsYouNotifications: NSObject, NeedsYouPosting {
    static let shared = NeedsYouNotifications()

    /// The workspace whose window is in front, if one is (the app sets it).
    var frontWorkspace: () -> String? = { nil }
    /// Brings a notification's workspace forward with its question shown.
    var show: (NotificationRoute) -> Void = { _ in }

    private(set) lazy var center = NeedsYouCenter(poster: self, inFront: { [weak self] box in
        NSApp.isActive && NSApp.keyWindow != nil && self?.frontWorkspace() == box
    })

    private static let log = Logger(subsystem: "com.portenv.Portenv", category: "notifications")

    /// At launch: the delegate (so a click that launched the app still
    /// arrives) and the categories, whose only action is Show. Doesn't ask
    /// for permission: that waits for the first notification (§5, PLAN
    /// first run).
    func install() {
        let un = UNUserNotificationCenter.current()
        un.delegate = NotificationDelegate.shared
        let show = UNNotificationAction(identifier: NotificationAction.show.rawValue, title: "Show", options: [.foreground])
        un.setNotificationCategories([
            UNNotificationCategory(identifier: NeedsYouNotice.decisionCategory, actions: [show], intentIdentifiers: []),
            UNNotificationCategory(identifier: NeedsYouNotice.waitingCategory, actions: [show], intentIdentifiers: []),
        ])
    }

    func post(_ n: NeedsYouNotice) {
        let content = UNMutableNotificationContent()
        content.title = n.content.title
        content.subtitle = n.content.subtitle
        content.body = n.content.body
        content.categoryIdentifier = n.category
        content.threadIdentifier = n.route.box
        content.userInfo = n.route.userInfo
        // No sound by default (§5).
        let request = UNNotificationRequest(identifier: n.id, content: content, trigger: nil)
        let kind = n.category
        Task {
            let un = UNUserNotificationCenter.current()
            // Asked the first time something is worth notifying, never at
            // launch; denied, nothing is posted and nothing nags.
            guard (try? await un.requestAuthorization(options: [.alert])) == true else { return }
            do {
                try await un.add(request)
                Self.log.notice("posted a \(kind, privacy: .public) notification")
            } catch {
                Self.log.error("a \(kind, privacy: .public) notification wasn't posted: \(error.localizedDescription, privacy: .public)")
            }
        }
    }

    func withdraw(_ ids: [String]) {
        let un = UNUserNotificationCenter.current()
        un.removeDeliveredNotifications(withIdentifiers: ids)
        un.removePendingNotificationRequests(withIdentifiers: ids)
    }

    func setBadge(_ label: String?) {
        NSApp.dockTile.badgeLabel = label
    }
}

/// Shows Portenv's notifications while it's active (another workspace's
/// tab in front still gets them, §5) and routes their clicks.
final class NotificationDelegate: NSObject, UNUserNotificationCenterDelegate, Sendable {
    static let shared = NotificationDelegate()

    func userNotificationCenter(_: UNUserNotificationCenter, willPresent _: UNNotification) async
        -> UNNotificationPresentationOptions {
        [.banner, .list]
    }

    func userNotificationCenter(_: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        // A click or Show: the only actions there are. Dismissing does
        // nothing.
        let action = response.actionIdentifier
        guard action == UNNotificationDefaultActionIdentifier || action == NotificationAction.show.rawValue else { return }
        let info = response.notification.request.content.userInfo
        let route = NotificationRoute(userInfo: info)
        await MainActor.run {
            NSApp.activate()
            if let route { NeedsYouNotifications.shared.show(route) }
        }
    }
}
