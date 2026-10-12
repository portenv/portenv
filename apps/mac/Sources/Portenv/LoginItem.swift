// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
import ServiceManagement

/// portenvd as the app's login item (1.1): launchd runs it from the app
/// bundle (Contents/Library/LaunchAgents/com.portenv.portenvd.plist,
/// KeepAlive) and starts it again whenever it exits, after a crash or an
/// update relaunch. The app never starts portenvd itself, so portenvd is
/// never the app's child.
enum LoginItem {
    /// The launch agent: com.portenv.portenvd, or a dev lane's own (gmake
    /// app LANE=X), so lanes never share a login item.
    static var service: SMAppService { SMAppService.agent(plistName: Instance.current.daemonPlistName) }

    static let needsApproval = "Portenv needs to run in the background to keep your workspaces saved. Open System Settings › General › Login Items and allow Portenv."

    /// Registers the launch agent when it isn't. Returns a plain problem to
    /// show, or nil when launchd has it.
    @Sendable static func ensureRegistered() async -> String? {
        // Moving to Applications: never register the old place again
        // (R-0125).
        guard await MoveToApplications.gate.registrationAllowed else { return nil }
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

    /// Unregisters the launch agent (dev builds and tests): launchd stops
    /// portenvd and won't start it at login. Returns an exit status.
    static func unregister() -> Int32 {
        let done = DispatchSemaphore(value: 0)
        var status: Int32 = 0
        service.unregister { error in
            if let error {
                FileHandle.standardError.write(Data("unregister: \(error.localizedDescription)\n".utf8))
                status = 1
            }
            done.signal()
        }
        done.wait()
        if status == 0 { print("Portenv's background service is unregistered") }
        return status
    }
}
