// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
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
    /// portenvd didn't answer in time: said plainly, with Try Again.
    private(set) var problem: String?

    private let daemon = SharedDaemon.client
    private let defaults = UserDefaults.standard
    /// Set once the box's window exists, so quitting closes the box.
    var onOpen: ((BoxController) -> Void)?

    static let lastBoxKey = "lastBox"
    static let firstRunDoneKey = "firstRunDone"

    /// At launch: make sure portenvd runs, list the boxes, and decide.
    func start() async {
        problem = nil
        _ = await LoginItem.ensureRegistered()
        let deadline = ContinuousClock.now + .seconds(30)
        while !(await daemon.ping()) {
            if let issue = await LoginItem.ensureRegistered() {
                problem = issue
            } else if ContinuousClock.now >= deadline {
                problem = BoxController.daemonGone
                return
            }
            try? await Task.sleep(for: .milliseconds(200))
        }
        problem = nil
        do {
            boxes = try await daemon.boxes()
        } catch {
            problem = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
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

    /// The box list's New Box…: first run's first-box step.
    func newBox() {
        firstRun = FirstRunFlow(service: AppFirstRunService(), at: .firstBox, existing: boxes)
        launch = .firstRun(.firstBox)
    }
}

/// Keys, the recovery key and creating a box from the app aren't built yet:
/// they're data-loss paths and wait for their ADR and tests (PLAN.md 1.5,
/// Open questions). Until then first run says so plainly instead of
/// pretending.
struct AppFirstRunService: FirstRunService {
    static let notYet = "Portenv can't make keys or boxes from the app yet. For now, make a box in Terminal with portenv init, then open Portenv again."
    func makeRecoveryKey() async throws -> String { throw FirstRunError(Self.notYet) }
    func createBox(_: NewBox) async throws { throw FirstRunError(Self.notYet) }
}

/// The window's content, from the model's decision.
struct AppRoot: View {
    @Bindable var model: AppModel

    var body: some View {
        Group {
            if let problem = model.problem {
                VStack(spacing: 12) {
                    Text(problem).font(.title3).multilineTextAlignment(.center).frame(maxWidth: 460)
                    Button("Try Again") { Task { await model.start() } }
                        .keyboardShortcut(.defaultAction)
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
                        FirstRunView(flow: flow) { box in Task { await model.open(box) } }
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
