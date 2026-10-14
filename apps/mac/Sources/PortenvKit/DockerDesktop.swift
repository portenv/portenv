// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Whether boxes can run on this Mac for now: Docker Desktop installed and
/// answering (R-0098). Welcome shows a card until it does.
public enum DockerStatus: Equatable, Sendable {
    case running, starting, notRunning, missing

    /// From portenvd's engine check (nil: portenvd didn't answer), whether
    /// Docker Desktop is installed, and whether it's open. The engine
    /// answering wins: it can run boxes, whatever app provides it. Open but
    /// not answering is starting: its engine isn't ready yet (R-0211). No
    /// answer is no outcome.
    public static func from(engine: Bool?, desktopInstalled: Bool, desktopRunning: Bool = false) -> DockerStatus? {
        switch engine {
        case true?: .running
        case false?: desktopRunning ? .starting : desktopInstalled ? .notRunning : .missing
        case nil: nil
        }
    }

    /// Whether Docker Desktop counts as installed, from where Launch
    /// Services finds it: a copy in a Trash doesn't (R-0110).
    public static func installed(at app: URL?) -> Bool {
        guard let app else { return false }
        let parts = app.standardizedFileURL.pathComponents
        return !parts.contains(".Trash") && !parts.contains(".Trashes")
    }

    /// What VoiceOver hears when the outcome changes (R-0110): a card's
    /// title when one appears or changes, "Docker Desktop is running" only
    /// when a card goes because Docker answered, and nothing otherwise (so
    /// someone with Docker already running hears nothing on Welcome).
    public static func announcement(from old: DockerStatus?, to new: DockerStatus?) -> String? {
        if let card = new?.card, old != new { return card.title }
        if new == .running, old?.card != nil { return DockerText.runningAnnouncement }
        return nil
    }

    /// Welcome's card for this outcome; none once Docker answers.
    public var card: DockerCard? {
        switch self {
        case .running: nil
        case .starting:
            DockerCard(title: DockerText.startingTitle, line: DockerText.startingLine, button: nil, action: nil)
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
    /// None while Docker Desktop starts: there's nothing to do but wait.
    public let button: String?
    public let action: Action?
}

/// The cards' words (R-0098; GUIDELINES §10).
public enum DockerText {
    public static let notRunningTitle = "Docker Desktop isn\u{2019}t running"
    public static let notRunningLine = "Portenv runs your workspaces in Docker Desktop for now. Open it, and Portenv carries on by itself."
    public static let openButton = "Open Docker Desktop"
    public static let missingTitle = "Portenv needs Docker Desktop for now"
    public static let missingLine = "Install Docker Desktop and open it once. Portenv carries on by itself. A later version won\u{2019}t need it."
    public static let getButton = "Get Docker Desktop\u{2026}"
    public static let checking = "Checking again by itself"
    /// Docker Desktop is open, its engine not ready yet (R-0211).
    public static let startingTitle = "Docker Desktop is starting\u{2026}"
    public static let startingLine = "Portenv carries on by itself as soon as it\u{2019}s ready."
    /// Before the first check has answered.
    public static let checkingFirst = "Checking Docker Desktop\u{2026}"
    /// What VoiceOver hears when the card goes because Docker answered.
    public static let runningAnnouncement = "Docker Desktop is running"
    /// The box's state line when Docker stops later, on this Mac.
    public static let stateLine = "This Mac \u{00B7} " + notRunningTitle

    public static let bundleID = "com.docker.docker"
    public static let download = URL(string: "https://www.docker.com/products/docker-desktop/")!
    /// How often Welcome checks again while the card shows.
    public static let interval: Duration = .seconds(2)
}
