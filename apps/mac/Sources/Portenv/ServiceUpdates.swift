// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
import ServiceManagement

/// The app's one ServiceUpdate (R-0198): this bundle's build against the
/// running portenvd's, restarting only this app's own login item.
@MainActor
enum ServiceUpdates {
    static let shared: ServiceUpdate = {
        let service: ServiceBuild = (SharedDaemon.client as? DaemonClient).map { DaemonServiceBuild(client: $0) } ?? NoService()
        let update = ServiceUpdate(appBuildID: ServiceUpdate.bundleBuildID, bundlePath: Bundle.main.bundlePath,
                                   service: service, restarter: LoginItemRestarter())
        DaemonClient.outOfDate.fix = { @MainActor in await update.ensureCurrent() }
        return update
    }()
}

/// This lane's or the release's own launch agent, never another's.
private struct LoginItemRestarter: ServiceRestarter {
    func gentle() async { try? await SharedDaemon.client.relaunch() }
    func hard() async {
        try? await LoginItem.service.unregister()
        _ = await LoginItem.ensureRegistered()
    }
}

private struct NoService: ServiceBuild {
    func running() async -> RunningService? { nil }
}
