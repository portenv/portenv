// SPDX-License-Identifier: Apache-2.0

import Foundation
import ServiceManagement

/// portenvd as the app's login item (1.1): launchd runs it from the app
/// bundle (Contents/Library/LaunchAgents/com.portenv.portenvd.plist,
/// KeepAlive) and starts it again whenever it exits, after a crash or an
/// update relaunch. The app never starts portenvd itself, so portenvd is
/// never the app's child.
enum LoginItem {
    static var service: SMAppService { SMAppService.agent(plistName: "com.portenv.portenvd.plist") }

    static let needsApproval = "Portenv needs to run in the background to keep your boxes saved. Open System Settings › General › Login Items and allow Portenv."

    /// Registers the launch agent when it isn't. Returns a plain problem to
    /// show, or nil when launchd has it.
    @Sendable static func ensureRegistered() async -> String? {
        switch service.status {
        case .enabled:
            return nil
        case .requiresApproval:
            return needsApproval
        default:
            do {
                try service.register()
            } catch {
                return "Portenv couldn't set up its background service: \(error.localizedDescription)"
            }
            return service.status == .requiresApproval ? needsApproval : nil
        }
    }
}
