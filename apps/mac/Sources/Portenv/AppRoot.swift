// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import ServiceManagement
import SwiftUI

/// What the windows show (PLAN.md 1.5): first run with no box, the box list
/// when the box asked for isn't here, and the box itself otherwise. Never a
/// window for a box that doesn't exist. Each workspace has a window of its
/// own, grouped as macOS window tabs, with its own session
/// (WorkspaceSession, R-0344); the first window shows the launch.
@MainActor
@Observable
final class AppModel {
    /// The launch's decision, for the first window (its session's
    /// `.launch` screen).
    private(set) var launch: Launch?
    private(set) var boxes: [String] = []
    /// The workspaces running on this Mac, with or without a window (ADR
    /// 0023): the list shows them as "Running", with Close Workspace.
    private(set) var running: Set<String> = []
    /// Every window's session; the menus act on `sessions.current`.
    let sessions = WorkspaceSessions()
    /// The first window's session, which shows the launch.
    private(set) weak var launchSession: WorkspaceSession?
    private var launchClaimed = false
    /// The workspaces known to be running when portenvd last answered, and
    /// those open in a window here: what Quit names when portenvd doesn't
    /// answer (R-0252).
    var knownRunning: [String] { sessions.knownRunning(also: running) }
    /// The launch window's first run (New Workspace… from another window's
    /// list is that session's own, `newBox`).
    private(set) var firstRun: FirstRunFlow?
    /// Why portenvd isn't there, said plainly: with Open Login Items when it
    /// waits for approval, with Try Again otherwise.
    private(set) var problem: BackgroundServiceProblem?

    private let daemon = SharedDaemon.client
    private let defaults = UserDefaults.standard

    /// A new window's session. Its controller brings this window (its tab)
    /// forward before a read that may raise a Keychain prompt (R-0214).
    static func makeSession() -> WorkspaceSession {
        let ref = SessionRef()
        let daemon = SharedDaemon.client
        let session = WorkspaceSession(daemon: daemon) { box in
            BoxController(box: box, daemon: daemon, notifier: Notifier.shared, ensureDaemon: LoginItem.ensureRegistered,
                          bringForward: {
                              Task { @MainActor in
                                  NSApp.activate()
                                  ref.session?.bringForward()
                              }
                          })
        }
        ref.session = session
        return session
    }

    /// A window's session, once it's in its window: the first one shows
    /// the launch and starts it.
    func attach(_ session: WorkspaceSession) {
        sessions.add(session)
        guard !launchClaimed else { return }
        // A notification is for a workspace whose tab isn't in front.
        Notifier.frontWorkspace = { [sessions] in
            sessions.all.first { $0.window != nil && $0.window === NSApp.mainWindow }?.box
        }
        launchClaimed = true
        launchSession = session
        session.screen = .launch
        // One launch wait at a time; Try Again restarts it.
        begin()
    }

    /// A window closed: its session ends; the workspace keeps running
    /// (ADR 0023).
    func detach(_ session: WorkspaceSession) {
        if let box = session.box { NeedsYouNotifications.shared.center.closed(box: box) }
        sessions.remove(session)
    }

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
            if let s = launchSession { Task { await open(box, in: s) } }
        case .boxList:
            break
        }
    }

    /// Opens a box that exists in a window: its own controllers, then the
    /// same sequence the window always ran (1.1). A workspace already open
    /// in another window brings that tab forward instead; a new tab that
    /// was only picking goes (R-0344).
    func open(_ box: String, in s: WorkspaceSession) async {
        // Never in a window that has closed meanwhile.
        guard sessions.all.contains(where: { $0 === s }) else { return }
        let flow = s.screen == .launch ? firstRun : s.newBox
        guard boxes.contains(box) || flow?.made == box else {
            // Never "Opening…" for ever (R-0386): the list, saying so.
            if s === launchSession, launch == .open(box), s.controller == nil { launch = LaunchGap.boxGone(box) }
            return
        }
        if flow?.made == box { defaults.set(true, forKey: Self.firstRunDoneKey) }
        if case let .focus(other) = sessions.choose(box, for: s) {
            other.bringForward()
            if s.controller == nil, let w = s.window, WindowCloseHook.hookedWindows.count > 1 {
                WindowCloseHook.requestClose(w)
            }
            return
        }
        if s.screen == .launch { firstRun = nil }
        let controller = s.show(box)
        guard let tabs = s.tabs else { return }
        // A long command finishing in a tab, when the window isn't in
        // front (§5).
        tabs.commandFinished = { [weak controller] f in
            let place = controller?.state?.location.map(ServerName.display)
            Task { await Notifier.shared.commandFinished(box: box, finished: f, location: place) }
        }
        // This session's decisions and tab questions notify, and count in
        // the Dock badge, whether its tab is in front or not (§5, R-0384).
        let needsYou = NeedsYouNotifications.shared.center
        let team = s.team
        // Only while this window still shows it: nothing late after it left.
        team?.changed = { [weak controller, weak s] state in
            guard s?.box == box else { return }
            needsYou.team(box: box, state: state, location: controller?.state?.location.map(ServerName.display))
        }
        tabs.changed = { [weak controller, weak team, weak s] ts in
            guard s?.box == box else { return }
            needsYou.tabs(box: box, tabs: ts, team: team?.state.agents ?? [],
                          location: controller?.state?.location.map(ServerName.display))
        }
        if s === launchSession { launch = .open(box) }
        defaults.set(box, forKey: Self.lastBoxKey)
        // The team while the window is open, whether its tab is in front
        // or not, so the tab's Needs you count is always current; it
        // watches again after a restart or a move.
        if let team = s.team { s.keep(Task { await team.follow() }) }
        let opening = Task {
            await controller.waitForDaemon()
            // Boxes saved in the background after Quit Anyway: say so once.
            await controller.announceBackgroundSaves()
            // Before opening: with no network at all the box opens offline
            // at once instead of probing storage.
            await NetworkWatch.shared.start()
            await controller.open()
        }
        s.openTask = opening
        await opening.value
        guard s.controller === controller else { return } // closed meanwhile
        // The state line follows the state portenvd pushes (WatchBoxState).
        // Alongside, whether Docker Desktop stopped (R-0098).
        s.keep(Task {
            async let follow: Void = controller.follow()
            async let engine: Void = controller.watchEngine()
            _ = await (follow, engine)
        })
    }

    /// A window is closing. Closing the last workspace window goes back to
    /// "Your workspaces", and the workspace keeps running (ADR 0023):
    /// nothing is saved, released or stopped. Closing a workspace tab while
    /// other windows are open just closes it (the workspace keeps running).
    /// Close Workspace, Quit or Move closes it. Any other window just
    /// closes.
    func windowShouldClose(_ window: NSWindow, session s: WorkspaceSession) -> Bool {
        guard s.controller != nil else { return true }
        let others = WindowCloseHook.hookedWindows.filter { $0 !== window }.count
        switch WindowClose.action(showingBox: true, otherBoxWindows: others) {
        case .closeWindow:
            return true
        case .showListLeavingItRunning:
            if !s.closing {
                s.closing = true
                Task { await leaveToList(s) }
            }
            return false
        }
    }

    /// Back to the list, the workspace still running (once it has finished
    /// opening, so an open in progress is never cut short).
    private func leaveToList(_ s: WorkspaceSession) async {
        defer { s.closing = false }
        await s.openTask?.value
        await showList(s)
    }

    /// Close Workspace from the list: saved and released, asking on a
    /// failed save (Try Again, Close Anyway, Cancel), as quitting does.
    func closeWorkspace(_ box: String) async {
        _ = await WindowClose.run(QuitFlow(daemon: daemon, box: box, openHere: true), ask: AppDelegate.askOnClose,
                                  tell: AppDelegate.tell) {}
        await refreshRunning()
    }

    /// A notification's click (§5): the workspace's window in front, with
    /// the decision selected as its Needs-you row selects it, or the
    /// asking tab shown. A workspace no window shows any more just brings
    /// Portenv forward.
    func show(_ route: NotificationRoute) {
        guard let s = sessions.showing(route.box) else {
            (NSApp.mainWindow ?? NSApp.windows.first { $0.isVisible })?.makeKeyAndOrderFront(nil)
            return
        }
        s.bringForward()
        switch route {
        case .decision(_, let id):
            if let team = s.team, let selection = NotificationRoute.selection(decision: id, in: team.state) {
                team.selection = selection
            }
        case .tab(_, let id):
            s.team?.selection = .allTabs
            Task { await s.tabs?.select(id) }
        }
    }

    /// Reads which workspaces run on this Mac.
    func refreshRunning() async {
        running = Set(await RunningWorkspaces.here(daemon))
    }

    /// Back to "Your workspaces"; the workspace shown may still be running.
    private func showList(_ s: WorkspaceSession) async {
        // The window no longer follows it: out of the badge, its
        // notifications taken back (it keeps running, ADR 0023).
        if let box = s.box { NeedsYouNotifications.shared.center.closed(box: box) }
        s.leave()
        if s === launchSession { launch = .boxList(notice: nil) }
        if let listed = try? await daemon.boxes() { boxes = listed }
        await refreshRunning()
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
                                docker: nil, serviceGone: serviceGone, teamPlanner: daemon as? TeamPlanning)
        service.keychainWaiting = { [weak flow] on in Task { @MainActor in flow?.keychainWaiting = on } }
        return flow
    }

    /// The box list's New Box…: first run's first-box step, titled "New
    /// box", with Cancel back to the list. In the launch window it's the
    /// launch's first run; in any other window, that window's own.
    func newBox(in s: WorkspaceSession) {
        let flow = makeFlow(at: .firstBox, fromBoxList: true, memory: NoFirstRunMemory())
        if s.screen == .launch {
            firstRun = flow
            launch = .firstRun(.firstBox)
        } else {
            s.newBox = flow
            s.screen = .newBox
        }
    }

    /// First run with no flow (R-0386): makes the one it should have.
    func ensureFirstRunFlow(at step: FirstRunStep) {
        guard firstRun == nil, launch == .firstRun(step) else { return }
        firstRun = makeFlow(at: step, fromBoxList: false, memory: DefaultsFirstRunMemory())
    }

    /// Cancel, ⎋ or "Your boxes" from New Box…: back to the list.
    func cancelNewBox(in s: WorkspaceSession) {
        if s.screen == .launch {
            firstRun = nil
            launch = .boxList(notice: nil)
        } else {
            s.newBox = nil
            s.screen = .list
        }
    }

    /// Whether the window may be a tab: not while it shows the launch's
    /// first run (the first-run window keeps tabbing off).
    func windowTabs(_ s: WorkspaceSession) -> Bool {
        guard s.screen == .launch, s.controller == nil, case .firstRun = launch else { return true }
        return false
    }
}

/// First run's keys and workspaces (ADR 0019): the recovery key and pool
/// from KeyedFirstRunService, the workspace through BoxMaker and portenvd's
/// CreateBox. Without a connection to portenvd at all, it says so plainly.
struct UnavailableFirstRunService: FirstRunService {
    func makeRecoveryKey() async throws -> String { throw FirstRunError(FirstRunText.noBackgroundService) }
    func createBox(_: NewBox) async throws { throw FirstRunError(FirstRunText.noBackgroundService) }
}

/// A session, for a closure that must be Sendable (the Keychain's
/// bring-forward); read on the main actor only.
final class SessionRef: @unchecked Sendable {
    weak var session: WorkspaceSession?
}

/// One window's content: its own session (a workspace, the list, New
/// Workspace…), or the launch in the first window (R-0344).
struct AppRoot: View {
    @Bindable var model: AppModel
    /// This window's session: its workspace, tabs, team and panes.
    @State private var session = AppModel.makeSession()
    @Environment(\.openWindow) private var openWindow

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
            } else if let controller = session.controller, let tabs = session.tabs {
                MainWindow(controller: controller, tabs: tabs, team: session.team, split: session.split)
            } else {
                switch session.screen {
                case .launch:
                    launchContent
                case .newBox:
                    if let flow = session.newBox {
                        FirstRunView(flow: flow, opened: { box in Task { await model.open(box, in: session) } },
                                     cancel: { model.cancelNewBox(in: session) }, refill: model.refill)
                    } else {
                        // Never a bare background (R-0386).
                        list(notice: nil)
                    }
                case .list, .workspace:
                    if model.launch == nil {
                        starting
                    } else {
                        list(notice: nil)
                    }
                }
            }
        }
        .frame(minWidth: 640, minHeight: 440)
        .background(WindowCloseGuard(tabs: model.windowTabs(session)) { window in
            model.windowShouldClose(window, session: session)
        })
        // The window's tab: its title, and the menus' target while it's main.
        .background(SessionWindow(session: session, sessions: model.sessions))
        // The window closing ends its session, not the workspace (ADR 0023).
        .background(EndsWithWindow { model.detach(session) })
        .task {
            WorkspaceWindows.open = { openWindow(id: WorkspaceWindows.id) }
            model.attach(session)
        }
        .onAppear { model.windowAppeared() }
        .onDisappear { model.windowDisappeared() }
        // On the launch window, so it travels with it and never hides on
        // another Space; the launch carries on behind it (R-0121).
        .sheet(isPresented: Binding(get: { model.moveOffered && model.launchSession === session },
                                    set: { model.moveOffered = $0 })) {
            MoveOfferView { model.answerMove($0) }
        }
        .alert(model.moveError ?? "", isPresented: Binding(get: { model.moveError != nil && model.launchSession === session },
                                                          set: { if !$0 { model.moveError = nil } })) {
            Button("OK") { model.moveError = nil }
        }
    }

    /// Never without end: after about 4 s, Welcome or the list says the
    /// service isn't answering (R-0121).
    private var starting: some View {
        ProgressView().controlSize(.small)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .accessibilityElement()
            .accessibilityLabel(LaunchWait.starting)
    }

    /// The first window, from the launch's decision.
    @ViewBuilder private var launchContent: some View {
        switch model.launch {
        case nil:
            starting
        case let .firstRun(step):
            if let flow = model.firstRun {
                FirstRunView(flow: flow, opened: { box in Task { await model.open(box, in: session) } },
                             cancel: flow.fromBoxList ? { model.cancelNewBox(in: session) } : nil, refill: model.refill)
            } else {
                // Never a bare background (R-0386): the flow is made, and
                // says so meanwhile.
                TerminalOpening(line: launchGap)
                    .onAppear { model.ensureFirstRunFlow(at: step) }
            }
        case let .boxList(notice):
            list(notice: notice)
        case .open:
            // Opening: the workspace shows once its session has it, saying
            // so meanwhile (R-0386).
            TerminalOpening(line: launchGap)
        }
    }

    /// What the launch window says while it catches up (R-0386).
    private var launchGap: String {
        LaunchGap.line(model.launch, hasController: false, hasFirstRunFlow: model.firstRun != nil) ?? LaunchWait.starting
    }

    private func list(notice: String?) -> some View {
        BoxListView(boxes: model.boxes, notice: notice,
                    retry: model.serviceNotAnswering ? { model.begin() } : nil,
                    running: model.running,
                    close: { box in Task { await model.closeWorkspace(box) } },
                    refresh: { await model.refreshRunning() }) { box in
            Task { await model.open(box, in: session) }
        } newBox: {
            model.newBox(in: session)
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
