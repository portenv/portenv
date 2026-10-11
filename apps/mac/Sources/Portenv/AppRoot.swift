// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import ServiceManagement
import SwiftUI

/// What the window shows (PLAN.md 1.5): first run with no box, the box list
/// when the box asked for isn't here, and the box itself otherwise. Never a
/// window for a box that doesn't exist.
@MainActor
@Observable
final class AppModel {
    private(set) var launch: Launch?
    private(set) var boxes: [String] = []
    private(set) var controller: BoxController?
    private(set) var tabs: TabsController?
    private(set) var firstRun: FirstRunFlow?
    /// Why portenvd isn't there, said plainly: with Open Login Items when it
    /// waits for approval, with Try Again otherwise.
    private(set) var problem: BackgroundServiceProblem?

    private let daemon = SharedDaemon.client
    private let defaults = UserDefaults.standard
    /// Set once the box's window exists, so quitting closes the box.
    var onOpen: ((BoxController) -> Void)?

    static let lastBoxKey = "lastBox"
    static let firstRunDoneKey = "firstRunDone"

    /// At launch: offer to move to Applications, make sure portenvd runs,
    /// list the boxes, and decide.
    func start() async {
        problem = nil
        if MoveToApplications.offerIfNeeded() { return } // quitting: the copy opens
        _ = await LoginItem.ensureRegistered()
        let deadline = ContinuousClock.now + .seconds(30)
        var lastJobCheck = ContinuousClock.now - .seconds(10)
        while !(await daemon.ping()) {
            _ = await LoginItem.ensureRegistered()
            if ContinuousClock.now - lastJobCheck >= .seconds(2) {
                // The login item's status first (R-0028): waiting for
                // approval is said as such and the wait goes on, so the app
                // carries on once it's approved. launchd refusing an enabled
                // item (exit 78) is said at once and ends the wait.
                lastJobCheck = .now
                problem = BackgroundServiceProblem.of(
                    status: MoveToApplications.serviceStatus,
                    launchdJob: MoveToApplications.launchdJob(),
                    placement: MoveToApplications.placement
                )
                if let problem, problem.endsWait { return }
            }
            if problem == nil, ContinuousClock.now >= deadline {
                problem = BackgroundServiceProblem(message: BoxController.daemonGone, opensLoginItems: false, offersTryAgain: true)
                return
            }
            try? await Task.sleep(for: .milliseconds(200))
        }
        problem = nil
        do {
            boxes = try await daemon.boxes()
        } catch {
            problem = BackgroundServiceProblem(
                message: (error as? LocalizedError)?.errorDescription ?? error.localizedDescription,
                opensLoginItems: false,
                offersTryAgain: true
            )
            return
        }
        let requested = ProcessInfo.processInfo.environment["PORTENV_BOX"]
        apply(LaunchDecision.decide(
            requested: requested,
            boxes: boxes,
            lastBox: defaults.string(forKey: Self.lastBoxKey),
            firstRunDone: defaults.bool(forKey: Self.firstRunDoneKey)
        ))
    }

    private func apply(_ decision: Launch) {
        launch = decision
        switch decision {
        case let .firstRun(step):
            firstRun = FirstRunFlow(service: AppFirstRunService(), at: step, existing: boxes)
        case let .open(box):
            Task { await open(box) }
        case .boxList:
            break
        }
    }

    /// Opens a box that exists: its controllers, then the same sequence the
    /// window always ran (1.1).
    func open(_ box: String) async {
        guard boxes.contains(box) || firstRun?.made == box else { return }
        if firstRun?.made == box { defaults.set(true, forKey: Self.firstRunDoneKey) }
        firstRun = nil
        let controller = BoxController(
            box: box,
            daemon: daemon,
            notifier: Notifier.shared,
            ensureDaemon: LoginItem.ensureRegistered
        )
        self.controller = controller
        tabs = TabsController(box: box, daemon: daemon)
        launch = .open(box)
        defaults.set(box, forKey: Self.lastBoxKey)
        onOpen?(controller)
        await controller.waitForDaemon()
        // Boxes saved in the background after Quit Anyway: say so once.
        await controller.announceBackgroundSaves()
        // Before opening: with no network at all the box opens offline at
        // once instead of probing storage.
        await NetworkWatch.shared.start()
        await controller.open()
        // The state line follows the state portenvd pushes (WatchBoxState).
        await controller.follow()
    }

    /// The box list's New Box…: first run's first-box step, titled "New
    /// box", with Cancel back to the list.
    func newBox() {
        firstRun = FirstRunFlow(service: AppFirstRunService(), at: .firstBox, existing: boxes, fromBoxList: true)
        launch = .firstRun(.firstBox)
    }

    /// Cancel, ⎋ or "Your boxes" from New Box…: back to the list.
    func cancelNewBox() {
        firstRun = nil
        launch = .boxList(notice: nil)
    }
}

/// Keys, the recovery key and creating a box from the app aren't built yet:
/// they're data-loss paths and wait for their ADR and tests (PLAN.md 1.5,
/// Open questions). Until then first run says so plainly, as a notice:
/// dev builds name the CLI workaround; a release build never sends anyone
/// to Terminal (GUIDELINES §10).
struct AppFirstRunService: FirstRunService {
    /// Lane and other dev builds have their own bundle identifier.
    static var isDevBuild: Bool { Bundle.main.bundleIdentifier != AppMover.bundleID }
    static let notYetDev = "Dev build: making keys and boxes from the app isn’t built yet. Make a box with portenv init, then open Portenv again."
    static let notYet = "Making keys and boxes from the app isn’t ready yet."
    private var message: String { Self.isDevBuild ? Self.notYetDev : Self.notYet }
    func makeRecoveryKey() async throws -> String { throw FirstRunError(message, notBuilt: true) }
    func createBox(_: NewBox) async throws { throw FirstRunError(message, notBuilt: true) }
}

/// The window's content, from the model's decision.
struct AppRoot: View {
    @Bindable var model: AppModel

    var body: some View {
        Group {
            if let problem = model.problem {
                VStack(spacing: 12) {
                    Text(problem.message).font(.title3).multilineTextAlignment(.center).frame(maxWidth: 460)
                    HStack(spacing: 12) {
                        // Login Items first (R-0032). While it only waits for
                        // approval, Portenv carries on by itself once it's on.
                        if problem.opensLoginItems {
                            Button(ServiceText.openLoginItems) { SMAppService.openSystemSettingsLoginItems() }
                                .buttonStyle(.borderedProminent)
                                .keyboardShortcut(.defaultAction)
                        }
                        if problem.offersTryAgain {
                            let tryAgain = Button("Try Again") { Task { await model.start() } }
                            if problem.opensLoginItems {
                                tryAgain
                            } else {
                                tryAgain.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                            }
                        }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                switch model.launch {
                case nil:
                    ProgressView().controlSize(.small)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .accessibilityLabel("Starting Portenv")
                case .firstRun:
                    if let flow = model.firstRun {
                        FirstRunView(flow: flow, opened: { box in Task { await model.open(box) } },
                                     cancel: flow.fromBoxList ? { model.cancelNewBox() } : nil)
                    }
                case let .boxList(notice):
                    BoxListView(boxes: model.boxes, notice: notice) { box in
                        Task { await model.open(box) }
                    } newBox: {
                        model.newBox()
                    }
                case .open:
                    if let controller = model.controller, let tabs = model.tabs {
                        MainWindow(controller: controller, tabs: tabs)
                    }
                }
            }
        }
        .frame(minWidth: 640, minHeight: 440)
    }
}
