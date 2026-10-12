// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Whether boxes can run on this Mac for now: Docker Desktop installed and
/// answering (R-0098). Welcome shows a card until it does.
public enum DockerStatus: Equatable, Sendable {
    case running, notRunning, missing

    /// From portenvd's engine check (nil: portenvd didn't answer) and
    /// whether Docker Desktop is installed. The engine answering wins: it
    /// can run boxes, whatever app provides it. No answer is no outcome.
    public static func from(engine: Bool?, desktopInstalled: Bool) -> DockerStatus? {
        switch engine {
        case true?: .running
        case false?: desktopInstalled ? .notRunning : .missing
        case nil: nil
        }
    }

    /// Welcome's card for this outcome; none once Docker answers.
    public var card: DockerCard? {
        switch self {
        case .running: nil
        case .notRunning:
            DockerCard(title: DockerText.notRunningTitle, line: DockerText.notRunningLine,
                       button: DockerText.openButton, action: .openDockerDesktop)
        case .missing:
            DockerCard(title: DockerText.missingTitle, line: DockerText.missingLine,
                       button: DockerText.getButton, action: .getDockerDesktop)
        }
    }
}

/// The notice card on Welcome (the first-run-1-welcome-docker-* boards).
public struct DockerCard: Equatable, Sendable {
    public enum Action: Equatable, Sendable { case openDockerDesktop, getDockerDesktop }
    public let title: String
    public let line: String
    public let button: String
    public let action: Action
}

/// The cards' words (R-0098; GUIDELINES §10).
public enum DockerText {
    public static let notRunningTitle = "Docker Desktop isn\u{2019}t running"
    public static let notRunningLine = "Portenv runs your boxes in Docker Desktop for now. Open it, and Portenv carries on by itself."
    public static let openButton = "Open Docker Desktop"
    public static let missingTitle = "Portenv needs Docker Desktop for now"
    public static let missingLine = "Install Docker Desktop and open it once. Portenv carries on by itself. A later version won\u{2019}t need it."
    public static let getButton = "Get Docker Desktop\u{2026}"
    public static let checking = "Checking again by itself"
    /// The box's state line when Docker stops later, on this Mac.
    public static let stateLine = "This Mac \u{00B7} " + notRunningTitle

    public static let bundleID = "com.docker.docker"
    public static let download = URL(string: "https://www.docker.com/products/docker-desktop/")!
    /// How often Welcome checks again while the card shows.
    public static let interval: Duration = .seconds(2)
}
