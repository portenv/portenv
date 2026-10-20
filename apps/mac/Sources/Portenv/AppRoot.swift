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
    /// The workspaces running on this Mac, with or without a window (ADR
    /// 0023): the list shows them as "Running", with Close Workspace.
    private(set) var running: Set<String> = []
    private(set) var controller: BoxController?
    /// The workspaces known to be running when portenvd last answered, and
    /// the window's if it's open: what Quit names when portenvd doesn't
    /// answer (R-0252).
    var knownRunning: [String] {
        var names = running
        if let c = controller, c.isOpen { names.insert(c.box) }
        return names.sorted()
    }
    private(set) var tabs: TabsController?
    /// The open workspace's split view (PLAN 1.2b): one pane on each open.
    private(set) var split: SplitController?
    /// The open workspace's agent team (ADR 0022).
    private(set) var team: TeamController?
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

    /// Waits for portenvd, lists the boxes, and decides. Never a spinner
    /// without end (R-0121): after two missed checks (about 4 s) the window
    /// says the service isn't answering, with Try Again, and the checks go
    /// on.
    private func waitAndDecide() async {
        let outcome = await LaunchWait.run(
            ping: { [daemon] in
                if await daemon.ping() { return true }
                _ = await LoginItem.ensureRegistered()
                return false
            },
            // The login item's status first (R-0028): waiting for approval
            // is said as such and the wait goes on, so the app carries on
            // once it's approved. launchd refusing an enabled item (exit 78)
            // is said at once and ends the wait.
            problem: {
                BackgroundServiceProblem.of(
                    status: MoveToApplications.serviceStatus,
                    launchdJob: MoveToApplications.launchdJob(),
                    placement: MoveToApplications.placement
                )
            },
            report: { [weak self] report in self?.waiting(report) },
            sleep: { try? await Task.sleep(for: $0) },
            boxes: { [daemon] in
                // After an in-place update the running portenvd is still the
                // old one: restart it here, saying so, rather than "out of
                // date" later (R-0198).
                if case let .refused(message) = await ServiceUpdates.shared.ensureCurrent() { throw DaemonError(message) }
                let listed = try await daemon.boxes()
                // A workspace made outside first run (portenv init) ends it too (R-0133).
                DefaultsFirstRunMemory().forgetIfWorkspacesExist(listed)
                // Pool entries for workspaces that exist go, and a workspace left
                // half made is finished or discarded (ADR 0019, A3).
                guard let creating = daemon as? BoxCreating else { return listed }
                _ = await BoxMaker(daemon: creating).tidyAtLaunch()
                return (try? await daemon.boxes()) ?? listed
            },
            decide: { [weak self] listed in self?.answered(listed) }
        )
        switch outcome {
        case .decided, .cancelled:
            break
        case let .refused(p):
            problem = p
        case let .failed(message):
            problem = BackgroundServiceProblem(message: message, opensLoginItems: false, offersTryAgain: true)
        }
    }

    /// portenvd answered: decide, unless the person has moved on from the
    /// fallback screen, when the answer only refreshes the list (R-0125).
    private func answered(_ listed: [String]) {
        serviceNotAnswering = false
        problem = nil
        boxes = listed
        let decision = LaunchDecision.decide(
            requested: ProcessInfo.processInfo.environment["PORTENV_BOX"],
            boxes: listed,
            lastBox: defaults.string(forKey: Self.lastBoxKey),
            firstRunDone: defaults.bool(forKey: Self.firstRunDoneKey)
        )
        let applies = LaunchWait.applies(decision, fallback: fallbackShown, current: launch,
                                         firstRunStep: firstRun?.step)
        fallbackShown = nil
        if applies { apply(decision) }
        // Servers waiting for their host key to be confirmed: asked about
        // once per launch, after the window is up (ADR 0021, R-0180).
        Task { await ServerKeyAlert.runAtLaunch() }
    }

    /// The screen shown while portenvd wasn't answering, if any.
    private var fallbackShown: Launch?

    /// portenvd hasn't answered two checks in a row: true until it does.
    private(set) var serviceNotAnswering = false
    /// The running launch wait, so Try Again starts a fresh one.
    private var startTask: Task<Void, Never>?

    /// Starts (or, for Try Again, restarts) the launch: one wait at a
    /// time, ending at an answer.
    func begin() {
        startTask?.cancel()
        startTask = Task { await start() }
    }

    /// The windows showing the launch: the wait stops only when the last
    /// one closes (R-0125, R-0132).
    private var windows = LaunchWindows()

    func windowAppeared() { windows.appeared() }

    func windowDisappeared() {
        if windows.disappeared() { stopWaiting() }
    }

    /// The launch wait stops asking: the last window closed, or the app is
    /// quitting.
    func stopWaiting() {
        startTask?.cancel()
        startTask = nil
    }

    /// What the launch wait says, on the window (R-0121).
    private func waiting(_ report: LaunchWait.Report) {
        switch report {
        case let .problem(p):
            problem = p
        case .notAnswering:
            problem = nil
            serviceNotAnswering = true
            guard launch == nil else { return }
            let fallback = LaunchWait.fallback(lastBox: defaults.string(forKey: Self.lastBoxKey),
                                               firstRunDone: defaults.bool(forKey: Self.firstRunDoneKey))
            launch = fallback
            fallbackShown = fallback
            if case .firstRun = fallback {
                firstRun = makeFlow(at: .welcome, fromBoxList: false, memory: DefaultsFirstRunMemory(), serviceGone: true)
            }
        }
    }

    private func apply(_ decision: Launch) {
        launch = decision
        switch decision {
        case let .firstRun(step):
            // Docker Desktop is checked before Welcome's Continue and before
            // making the first workspace (R-0098, R-0211).
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
        split = SplitController(tabs: tabs)
        team = TeamController(box: box, daemon: daemon)
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
        // Alongside, whether Docker Desktop stopped (R-0098).
        followTask = Task {
            async let follow: Void = controller.follow()
            async let engine: Void = controller.watchEngine()
            _ = await (follow, engine)
        }
    }

    /// The window is closing. Closing the last workspace window goes back to
    /// "Your workspaces", and the workspace keeps running (ADR 0023): nothing
    /// is saved, released or stopped. Close Workspace, Quit or Move closes
    /// it. Any other window just closes.
    func windowShouldClose(_ window: NSWindow) -> Bool {
        guard case .open = launch, controller != nil else { return true }
        let others = WindowCloseHook.hookedWindows.filter { $0 !== window }.count
        switch WindowClose.action(showingBox: true, otherBoxWindows: others) {
        case .closeWindow:
            return true
        case .showListLeavingItRunning:
            if !closing {
                closing = true
                Task { await leaveToList() }
            }
            return false
        }
    }

    /// Back to the list, the workspace still running (once it has finished
    /// opening, so an open in progress is never cut short).
    private func leaveToList() async {
        defer { closing = false }
        await openTask?.value
        await showList()
    }

    /// Close Workspace from the list: saved and released, asking on a
    /// failed save (Try Again, Close Anyway, Cancel), as quitting does.
    func closeWorkspace(_ box: String) async {
        _ = await WindowClose.run(QuitFlow(daemon: daemon, box: box, openHere: true), ask: AppDelegate.askOnClose,
                                  tell: AppDelegate.tell) {}
        await refreshRunning()
    }

    /// Reads which workspaces run on this Mac.
    func refreshRunning() async {
        running = Set(await RunningWorkspaces.here(daemon))
    }

    /// Back to "Your workspaces"; the workspace shown may still be running.
    private func showList() async {
        followTask?.cancel()
        followTask = nil
        openTask = nil
        controller = nil
        tabs = nil
        split = nil
        onOpen?(nil)
        if let listed = try? await daemon.boxes() { boxes = listed }
        await refreshRunning()
        launch = .boxList(notice: nil)
    }

    /// A first-run flow with real keys, its Keychain waits said in the
    /// window.
    /// The recovery key sheet's refill, when portenvd can check keys.
    var refill: RecoveryRefill? {
        (daemon as? (BoxCreating & RecoveryChecking)).map { RecoveryRefill(daemon: $0) }
    }

    /// A first run, or New Box…; Docker Desktop's status is unknown until
    /// its first check answers (R-0098). serviceGone: reached after the
    /// launch's own two misses (R-0121).
    private func makeFlow(at step: FirstRunStep, fromBoxList: Bool, memory: FirstRunMemory, serviceGone: Bool = false) -> FirstRunFlow {
        guard let creating = daemon as? BoxCreating else {
            return FirstRunFlow(service: UnavailableFirstRunService(), at: step, existing: boxes, fromBoxList: fromBoxList, memory: memory,
                                docker: nil, serviceGone: serviceGone)
        }
        let service = KeyedFirstRunService(maker: BoxMaker(daemon: creating))
        // The Team step (ADR 0025) plans through portenvd.
        let flow = FirstRunFlow(service: service, at: step, existing: boxes, fromBoxList: fromBoxList, memory: memory,
                                docker: nil, serviceGone: serviceGone, teamPlanner: daemon as? TeamPlanning,
                                githubLogin: { GitHubAccount.system.login })
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
            if ServiceUpdates.shared.updating {
                ServiceUpdatingView()
            } else if let problem = model.problem {
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
                            let tryAgain = Button("Try Again") { model.begin() }
                            if problem.opensLoginItems {
                                tryAgain
                            } else {
                                tryAgain.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                            }
                        }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                // Back from System Settings: check again at once (R-0250).
                .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
                    model.begin()
                }
            } else {
                switch model.launch {
                case nil:
                    // Never without end: after about 4 s, Welcome or the
                    // list says the service isn't answering (R-0121).
                    ProgressView().controlSize(.small)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .accessibilityElement()
                        .accessibilityLabel(LaunchWait.starting)
                case .firstRun:
                    if let flow = model.firstRun {
                        FirstRunView(flow: flow, opened: { box in Task { await model.open(box) } },
                                     cancel: flow.fromBoxList ? { model.cancelNewBox() } : nil, refill: model.refill)
                    }
                case let .boxList(notice):
                    BoxListView(boxes: model.boxes, notice: notice,
                                retry: model.serviceNotAnswering ? { model.begin() } : nil,
                                running: model.running,
                                close: { box in Task { await model.closeWorkspace(box) } },
                                refresh: { await model.refreshRunning() }) { box in
                        Task { await model.open(box) }
                    } newBox: {
                        model.newBox()
                    }
                case .open:
                    if let controller = model.controller, let tabs = model.tabs {
                        MainWindow(controller: controller, tabs: tabs, team: model.team, split: model.split)
                    }
                }
            }
        }
        .frame(minWidth: 640, minHeight: 440)
        .background(WindowCloseGuard { window in model.windowShouldClose(window) })
        .onAppear { model.windowAppeared() }
        .onDisappear { model.windowDisappeared() }
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

/// While portenvd restarts after an update (R-0198).
struct ServiceUpdatingView: View {
    var body: some View {
        VStack(spacing: 12) {
            ProgressView().controlSize(.small)
            Text(ServiceUpdate.updatingText).font(.title3)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityElement(children: .combine)
    }
}
