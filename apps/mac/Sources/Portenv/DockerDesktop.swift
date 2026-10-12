// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Docker Desktop on this Mac (R-0098): whether it answers, through
/// portenvd (only its driver talks to Docker), and opening or getting it.
@MainActor
enum DockerDesktop {
    static func status() async -> DockerStatus? {
        let engine = await SharedDaemon.client.engineRunning()
        return DockerStatus.from(engine: engine, desktopInstalled: installedApp != nil)
    }

    /// Docker Desktop's app, unless it's missing or in the Trash.
    private static var installedApp: URL? {
        let app = NSWorkspace.shared.urlForApplication(withBundleIdentifier: DockerText.bundleID)
        return DockerStatus.installed(at: app) ? app : nil
    }

    static func perform(_ action: DockerCard.Action) {
        switch action {
        case .openDockerDesktop:
            guard let app = installedApp else {
                NSWorkspace.shared.open(DockerText.download)
                return
            }
            NSWorkspace.shared.openApplication(at: app, configuration: NSWorkspace.OpenConfiguration())
        case .getDockerDesktop:
            NSWorkspace.shared.open(DockerText.download)
        }
    }
}

/// Welcome's notice card (the first-run-1-welcome-docker-* boards): what's
/// wrong, the one button that fixes it, and that Portenv checks again.
struct DockerCardView: View {
    let card: DockerCard
    let act: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: "exclamationmark.triangle")
                .font(.title2)
                .foregroundStyle(.orange)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 8) {
                Text(card.title).font(.headline)
                Text(card.line)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 12) {
                    Button(card.button, action: act)
                        .controlSize(.large)
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text(DockerText.checking).foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }
                .padding(.top, 4)
            }
            Spacer(minLength: 0)
        }
        .padding(18)
        .background(RoundedRectangle(cornerRadius: 12).fill(.quaternary.opacity(0.5)))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(.separator))
        .accessibilityElement(children: .contain)
    }
}
