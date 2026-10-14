// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Welcome's Docker Desktop card and the state line when Docker stops
/// (R-0098; the first-run-1-welcome-docker-* boards).
@MainActor
struct DockerCardTests {
    /// The check's three outcomes: the engine answering wins; otherwise
    /// whether Docker Desktop is installed picks the card. portenvd not
    /// answering is no outcome at all.
    @Test func threeOutcomes() {
        #expect(DockerStatus.from(engine: true, desktopInstalled: true) == .running)
        #expect(DockerStatus.from(engine: true, desktopInstalled: false) == .running)
        #expect(DockerStatus.from(engine: false, desktopInstalled: true) == .notRunning)
        #expect(DockerStatus.from(engine: false, desktopInstalled: false) == .missing)
        #expect(DockerStatus.from(engine: nil, desktopInstalled: true) == nil)
    }

    /// The cards' words, exactly as R-0098 gives them.
    @Test func cardsSayWhatTheBoardsSay() {
        let notRunning = DockerStatus.notRunning.card
        #expect(notRunning?.title == "Docker Desktop isn\u{2019}t running")
        #expect(notRunning?.line == "Portenv runs your workspaces in Docker Desktop for now. Open it, and Portenv carries on by itself.")
        #expect(notRunning?.button == "Open Docker Desktop")
        #expect(notRunning?.action == .openDockerDesktop)

        let missing = DockerStatus.missing.card
        #expect(missing?.title == "Portenv needs Docker Desktop for now")
        #expect(missing?.line == "Install Docker Desktop and open it once. Portenv carries on by itself. A later version won\u{2019}t need it.")
        #expect(missing?.button == "Get Docker Desktop\u{2026}")
        #expect(missing?.action == .getDockerDesktop)

        #expect(DockerStatus.running.card == nil)
        #expect(DockerText.checking == "Checking again by itself")
        #expect(DockerText.bundleID == "com.docker.docker")
        #expect(DockerText.download.absoluteString == "https://www.docker.com/products/docker-desktop/")
    }

    /// Continue on Welcome only once Docker answers; before the first
    /// answer it's disabled too.
    @Test func continueWaitsForDocker() async {
        for (status, can) in [(nil, false), (DockerStatus.missing, false), (.notRunning, false), (.running, true)] {
            let flow = FirstRunFlow(service: FakeFirstRunService(), docker: status)
            #expect(flow.canContinue == can, "\(String(describing: status))")
            await flow.next()
            #expect(flow.step == (can ? .recoveryKey : .welcome), "\(String(describing: status))")
        }
    }

    /// The check runs at once, then every 2 s while the card shows, and
    /// stops as soon as Docker answers: the card goes, Continue enables.
    @Test func checksEveryTwoSecondsUntilDockerAnswers() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        let answers = Answers<DockerStatus?>([.missing, .notRunning, nil, .running, .missing])
        let slept = Answers<Duration>([])
        await flow.followDocker(check: { await answers.next() }, sleep: { await slept.add($0) })
        #expect(flow.docker == .running)
        #expect(flow.canContinue)
        #expect(await answers.taken == 4) // stopped at the first "running"
        #expect(await slept.all == [.seconds(2), .seconds(2), .seconds(2)])
    }

    /// portenvd not answering keeps the last outcome (no flicker); two
    /// misses in a row say the background service isn't answering (R-0110,
    /// R-0116); leaving Welcome stops the checks.
    @Test func noAnswerKeepsTheCardAndLeavingStops() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        let answers = Answers<DockerStatus?>([.notRunning, nil, nil])
        await flow.followDocker(check: { await answers.next() }, sleep: { _ in
            if await answers.taken == 2 {
                #expect(flow.docker == .notRunning)
                #expect(!flow.daemonGone) // one miss: no notice
            }
            if await answers.taken == 3 {
                #expect(flow.docker == .notRunning)
                #expect(flow.daemonGone)
                flow.docker = .running
                await flow.next() // Welcome → Recovery key
            }
        })
        #expect(flow.step == .recoveryKey)
        #expect(await answers.taken == 3)
    }

    /// No answer from portenvd before any outcome is never a silent dead
    /// end: after two misses in a row Welcome says the background service
    /// isn't answering, and Try Again's check clears it once portenvd
    /// answers (R-0110, R-0116).
    @Test func twoMissesSayTheServiceIsntAnswering() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        await flow.checkDocker { nil }
        #expect(!flow.daemonGone)
        await flow.checkDocker { nil }
        #expect(flow.daemonGone)
        #expect(flow.docker == nil)
        #expect(!flow.canContinue)
        await flow.checkDocker { .running }
        #expect(!flow.daemonGone)
        #expect(flow.canContinue)
    }

    /// Misses count only in a row: an answer between two misses resets
    /// the count (R-0116).
    @Test func missesCountOnlyInARow() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        await flow.checkDocker { nil }
        await flow.checkDocker { .notRunning }
        await flow.checkDocker { nil }
        #expect(!flow.daemonGone)
        await flow.checkDocker { nil }
        #expect(flow.daemonGone)
    }

    /// Try Again during a check in flight does nothing, so a late answer
    /// can't overtake a newer one or bring the notice back (R-0116).
    @Test func tryAgainWaitsForTheCheckInFlight() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        await flow.checkDocker { nil }
        let (release, released) = AsyncStream<Void>.makeStream()
        let first = Task { @MainActor in
            await flow.checkDocker {
                for await _ in release { break }
                return .notRunning
            }
        }
        while !flow.checkingDocker { await Task.yield() }
        var asked = false
        await flow.checkDocker { asked = true; return nil } // Try Again
        #expect(!asked)
        #expect(!flow.daemonGone) // the ignored miss isn't counted
        released.yield()
        await first.value
        #expect(flow.docker == .notRunning)
        #expect(!flow.daemonGone)
        #expect(!flow.checkingDocker)
    }

    /// Continue, disabled, tells VoiceOver why: the card's title or the
    /// service notice (R-0116). The notice going with a card still there
    /// announces the card again.
    @Test func continueSaysWhyItIsDisabled() async {
        let flow = FirstRunFlow(service: FakeFirstRunService(), docker: nil)
        #expect(flow.continueHint == nil) // before the first answer
        await flow.checkDocker { .notRunning }
        #expect(flow.continueHint == "Docker Desktop isn\u{2019}t running")
        await flow.checkDocker { nil }
        await flow.checkDocker { nil }
        #expect(flow.continueHint == "Portenv\u{2019}s background service isn\u{2019}t answering. It usually starts by itself in a few seconds.")
        #expect(flow.serviceNoticeAnnouncement == flow.continueHint)
        await flow.checkDocker { .notRunning }
        #expect(flow.serviceNoticeAnnouncement == "Docker Desktop isn\u{2019}t running")
        await flow.checkDocker { .running }
        #expect(flow.continueHint == nil)
        #expect(!FirstRunText.serviceNotAnswering.lowercased().contains("box"))
    }

    /// VoiceOver hears a card appear or change, and "Docker Desktop is
    /// running" only when a shown card goes: someone with Docker running
    /// hears nothing (R-0110).
    @Test func announcesOnlyAroundACard() {
        #expect(DockerStatus.announcement(from: nil, to: .running) == nil)
        #expect(DockerStatus.announcement(from: .running, to: .running) == nil)
        #expect(DockerStatus.announcement(from: nil, to: .notRunning) == "Docker Desktop isn\u{2019}t running")
        #expect(DockerStatus.announcement(from: .notRunning, to: .notRunning) == nil)
        #expect(DockerStatus.announcement(from: .notRunning, to: .missing) == "Portenv needs Docker Desktop for now")
        #expect(DockerStatus.announcement(from: .notRunning, to: .running) == "Docker Desktop is running")
        #expect(DockerStatus.announcement(from: .missing, to: .running) == "Docker Desktop is running")
        #expect(DockerStatus.announcement(from: .notRunning, to: nil) == nil)
    }

    /// Docker Desktop in the Trash counts as missing (R-0110).
    @Test func dockerDesktopInTheTrashIsMissing() {
        #expect(DockerStatus.installed(at: URL(fileURLWithPath: "/Applications/Docker.app")))
        #expect(DockerStatus.installed(at: URL(fileURLWithPath: "/Users/a/Applications/Docker.app")))
        #expect(!DockerStatus.installed(at: URL(fileURLWithPath: "/Users/a/.Trash/Docker.app")))
        #expect(!DockerStatus.installed(at: URL(fileURLWithPath: "/Volumes/X/.Trashes/501/Docker.app")))
        #expect(!DockerStatus.installed(at: nil))
    }

    /// watchEngine ends when its task is cancelled (the window closing).
    @Test func watchEngineStopsWhenCancelled() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli, sleep: { _ in await Task.yield() })
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_AGENT_UNAVAILABLE"}"#
        await c.refresh()
        let watch = Task { await c.watchEngine() }
        while cli.calls.filter({ $0 == ["app", "engine"] }).count < 2 { await Task.yield() }
        watch.cancel()
        await watch.value // returns: the loop saw the cancellation
        let asked = cli.calls.filter { $0 == ["app", "engine"] }.count
        for _ in 0..<50 { await Task.yield() }
        #expect(cli.calls.filter { $0 == ["app", "engine"] }.count == asked) // no more checks
    }

    /// If Docker stops later, the box's state line says so on this Mac, and
    /// goes back to the normal line once Docker answers.
    @Test func theStateLineSaysWhenDockerStopped() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_AGENT_UNAVAILABLE"}"#
        await c.refresh()
        cli.engine = false
        await c.checkEngine()
        #expect(c.engineDown)
        #expect(c.subtitle == "This Mac \u{00B7} Docker Desktop isn\u{2019}t running")

        cli.engine = true
        await c.checkEngine()
        #expect(!c.engineDown)
        #expect(c.subtitle == "This Mac \u{00B7} Not saved")
    }

    /// Only when the box is open here and its agent is unavailable: a
    /// working box never asks, and a box on a server never says it.
    @Test func onlyAskedWhenTheAgentIsUnavailableHere() async {
        let cli = FakeDaemon()
        cli.engine = false
        let c = BoxController(box: "demo", daemon: cli)
        await c.open()
        await c.checkEngine()
        #expect(!c.engineDown)
        #expect(!cli.calls.contains(["app", "engine"]))

        cli.state = #"{"state":"SAVE_STATE_AGENT_UNAVAILABLE","location":"portenv@server-a"}"#
        await c.refresh()
        await c.checkEngine()
        #expect(!c.engineDown)
    }
}

/// Hands out answers in order (then the last one again), counting them.
actor Answers<T: Sendable> {
    private var items: [T]
    private(set) var taken = 0
    private(set) var all: [T] = []
    init(_ items: [T]) { self.items = items }
    func next() -> T {
        defer { taken += 1 }
        return taken < items.count ? items[taken] : items[items.count - 1]
    }
    func add(_ item: T) { all.append(item) }
}
