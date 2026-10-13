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
    /// Set once the box's window exists, so quitting closes the box; nil
    /// once the window has gone back to the box list.
    var onOpen: ((BoxController?) -> Void)?
    /// Opening the box (up to following its state), and following it.
    private var openTask: Task<Void, Never>?
    private var followTask: Task<Void, Never>?
    /// Closing the box to go back to the list: one close at a time.
    private(set) var closing = false

    static let lastBoxKey = "lastBox"
    static let firstRunDoneKey = "firstRunDone"

    /// "Move Portenv to Applications?" is showing, as a sheet on the window.
    var moveOffered = false
    /// Why a move failed, shown once.
    var moveError: String?

    /// At launch: make sure portenvd runs, offer to move to Applications
    /// alongside (a sheet; nothing waits for the answer, R-0121), list the
    /// boxes, and decide.
    func start() async {
        problem = nil
        await LaunchOrder.begin(
            offersMove: MoveToApplications.shouldOffer,
            register: { _ = await LoginItem.ensureRegistered() },
            offer: {
                // Never on a window that's off screen.
                OnScreen.fixAll()
                moveOffered = true
            },
            wait: { await waitAndDecide() }
        )
    }

    /// The sheet's answer: Move quits once the copy opens.
    func answerMove(_ answer: MoveOffer.Answer) {
        moveOffered = false
        Task { moveError = await MoveToApplications.answer(answer) }
    }

    /// Waits for portenvd, lists the boxes, and decides.
    private func waitAndDecide() async {
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
        // A workspace made outside first run (portenv init) ends it too (R-0133).
        DefaultsFirstRunMemory().forgetIfWorkspacesExist(boxes)
        // Pool entries for workspaces that exist go, and a workspace left
        // half made is finished or discarded (ADR 0019, A3).
        if let creating = daemon as? BoxCreating {
            _ = await BoxMaker(daemon: creating).tidyAtLaunch()
            if let listed = try? await daemon.boxes() { boxes = listed }
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
            firstRun = makeFlow(at: step, fromBoxList: false, memory: DefaultsFirstRunMemory())
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
        let tabs = TabsController(box: box, daemon: daemon)
        // A long command finishing in a tab, when the window isn't in
        // front (§5).
        tabs.commandFinished = { [weak controller] f in
            let place = controller?.state?.location.map(ServerName.display)
            Task { await Notifier.shared.commandFinished(box: box, finished: f, location: place) }
        }
        self.tabs = tabs
        launch = .open(box)
        defaults.set(box, forKey: Self.lastBoxKey)
        onOpen?(controller)
        let opening = Task {
            await controller.waitForDaemon()
            // Boxes saved in the background after Quit Anyway: say so once.
            await controller.announceBackgroundSaves()
            // Before opening: with no network at all the box opens offline
            // at once instead of probing storage.
            await NetworkWatch.shared.start()
            await controller.open()
        }
        openTask = opening
        await opening.value
        guard self.controller === controller else { return } // closed meanwhile
        // The state line follows the state portenvd pushes (WatchBoxState).
        followTask = Task { await controller.follow() }
    }

    /// The window is closing. Closing the last box window goes back to
    /// "Your boxes" instead of quitting (the owner, 2026-10-11): the box is
    /// closed, saved and released first, exactly as quitting does, and the
    /// window stays, showing the list. Any other window just closes.
    func windowShouldClose(_ window: NSWindow) -> Bool {
        guard case .open = launch, let controller else { return true }
        let others = WindowCloseHook.hookedWindows.filter { $0 !== window }.count
        switch WindowClose.action(showingBox: true, otherBoxWindows: others) {
        case .closeWindow:
            return true
        case .closeBoxThenShowList:
            if !closing {
                closing = true
                Task { await closeToList(controller) }
            }
            return false
        }
    }

    /// Closes the box (waiting for it to finish opening first, so a box
    /// still opening is never left open with no window), then the list.
    private func closeToList(_ controller: BoxController) async {
        defer { closing = false }
        await openTask?.value
        _ = await WindowClose.run(controller.quitFlow, ask: AppDelegate.askOnClose,
                                  closing: { on in await controller.showClosing(on) }) { [weak self] in
            await self?.showList()
        }
    }

    /// Back to "Your boxes", with the box closed.
    private func showList() async {
        followTask?.cancel()
        followTask = nil
        openTask = nil
        controller = nil
        tabs = nil
        onOpen?(nil)
        if let listed = try? await daemon.boxes() { boxes = listed }
        launch = .boxList(notice: nil)
    }

    /// A first-run flow with real keys, its Keychain waits said in the
    /// window.
    /// The recovery key sheet's refill, when portenvd can check keys.
    var refill: RecoveryRefill? {
        (daemon as? (BoxCreating & RecoveryChecking)).map { RecoveryRefill(daemon: $0) }
    }

    private func makeFlow(at step: FirstRunStep, fromBoxList: Bool, memory: FirstRunMemory) -> FirstRunFlow {
        guard let creating = daemon as? BoxCreating else {
            return FirstRunFlow(service: UnavailableFirstRunService(), at: step, existing: boxes, fromBoxList: fromBoxList, memory: memory)
        }
        let service = KeyedFirstRunService(maker: BoxMaker(daemon: creating))
        let flow = FirstRunFlow(service: service, at: step, existing: boxes, fromBoxList: fromBoxList, memory: memory)
        service.keychainWaiting = { [weak flow] on in Task { @MainActor in flow?.keychainWaiting = on } }
        return flow
    }

    /// The box list's New Box…: first run's first-box step, titled "New
    /// box", with Cancel back to the list.
    func newBox() {
        firstRun = makeFlow(at: .firstBox, fromBoxList: true, memory: NoFirstRunMemory())
        launch = .firstRun(.firstBox)
    }

    /// Cancel, ⎋ or "Your boxes" from New Box…: back to the list.
    func cancelNewBox() {
        firstRun = nil
        launch = .boxList(notice: nil)
    }
}

/// First run's keys and workspaces (ADR 0019): the recovery key and pool
/// from KeyedFirstRunService, the workspace through BoxMaker and portenvd's
/// CreateBox. Without a connection to portenvd at all, it says so plainly.
struct UnavailableFirstRunService: FirstRunService {
    func makeRecoveryKey() async throws -> String { throw FirstRunError(FirstRunText.noBackgroundService) }
    func createBox(_: NewBox) async throws { throw FirstRunError(FirstRunText.noBackgroundService) }
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
                                     cancel: flow.fromBoxList ? { model.cancelNewBox() } : nil, refill: model.refill)
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
        .background(WindowCloseGuard { window in model.windowShouldClose(window) })
        // On the window, so it travels with it and never hides on another
        // Space; the launch carries on behind it (R-0121).
        .sheet(isPresented: $model.moveOffered) {
            MoveOfferView { model.answerMove($0) }
        }
        .alert(model.moveError ?? "", isPresented: Binding(get: { model.moveError != nil },
                                                          set: { if !$0 { model.moveError = nil } })) {
            Button("OK") { model.moveError = nil }
        }
    }
}
