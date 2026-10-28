// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// The box's save state as portenvd records it (`portenv app state`): the
/// only source of what the title says about saving (docs/PLAN.md, 1.4).
public struct BoxState: Equatable, Sendable {
    public enum Save: String, Sendable {
        case notSavedYet = "SAVE_STATE_NOT_SAVED_YET"
        case saving = "SAVE_STATE_SAVING"
        case saved = "SAVE_STATE_SAVED"
        case offline = "SAVE_STATE_OFFLINE"
        case agentUnavailable = "SAVE_STATE_AGENT_UNAVAILABLE"
        case closed = "SAVE_STATE_CLOSED"
        case retrying = "SAVE_STATE_RETRYING"
        case notSaved = "SAVE_STATE_NOT_SAVED"
        case quitUnsaved = "SAVE_STATE_QUIT_UNSAVED"
    }

    public var save: Save
    public var savedAt: Date?
    /// The server the box runs on; nil for this Mac.
    public var location: String?
    /// Packages from apt-packages.txt that couldn't be installed (the
    /// inspector's Where it is section; never the state line).
    public var failedPackages: [String] = []
    /// portenvd restarted while the box was open: it isn't closed, but the
    /// new portenvd hasn't opened it and has none of its keys.
    public var interrupted = false
    /// Where the box's saves go, as a place name ("this Mac", a drive, a
    /// storage server's host): the inspector's "Saves go to". Nil if unknown.
    public var storagePlace: String?
    /// When portenvd opened the box (the inspector's "Since").
    public var openedAt: Date?
    /// This open stopped and started the box again to apply a security
    /// update (R-0201): the window tells the person once.
    public var restartedForSecurityUpdate = false
    /// The first open's image pull, while it runs (R-0211).
    public struct ImagePull: Equatable, Sendable {
        public let done: Int64
        public let total: Int64
        public init(done: Int64, total: Int64) {
            self.done = done
            self.total = total
        }
    }

    public var imagePull: ImagePull?

    public init(save: Save, savedAt: Date? = nil, location: String? = nil) {
        self.save = save
        self.savedAt = savedAt
        self.location = location
    }

    /// Parses `portenv app state` output.
    public static func parse(_ json: String) -> BoxState? {
        struct Wire: Decodable {
            let state: String
            let saved_at: Date?
            let location: String?
            let failed_packages: [String]?
            let interrupted: Bool?
            let storage_place: String?
            let opened_at: Date?
            let restarted_for_security_update: Bool?
            let image_pull: Pull?
            struct Pull: Decodable {
                // protojson writes int64 as a string; tests write numbers.
                let done_bytes: Flex?
                let total_bytes: Flex?
            }

            struct Flex: Decodable {
                let value: Int64
                init(from decoder: Decoder) throws {
                    let c = try decoder.singleValueContainer()
                    if let n = try? c.decode(Int64.self) { value = n } else { value = Int64(try c.decode(String.self)) ?? 0 }
                }
            }
        }
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = f.date(from: s) { return date }
            f.formatOptions = [.withInternetDateTime]
            if let date = f.date(from: s) { return date }
            throw DecodingError.dataCorruptedError(in: try dec.singleValueContainer(), debugDescription: "date")
        }
        guard let w = try? d.decode(Wire.self, from: Data(json.utf8)), let save = Save(rawValue: w.state) else { return nil }
        var s = BoxState(save: save, savedAt: w.saved_at, location: (w.location?.isEmpty ?? true) ? nil : w.location)
        s.failedPackages = w.failed_packages ?? []
        s.interrupted = w.interrupted ?? false
        s.storagePlace = (w.storage_place?.isEmpty ?? true) ? nil : w.storage_place
        s.openedAt = w.opened_at
        s.restartedForSecurityUpdate = w.restarted_for_security_update ?? false
        if let p = w.image_pull { s.imagePull = ImagePull(done: p.done_bytes?.value ?? 0, total: p.total_bytes?.value ?? 0) }
        return s
    }

}

/// One box's window: where the box is, what the title's state line says,
/// and the box actions. Every action goes through portenvd.
@MainActor
@Observable
public final class BoxController {
    /// Where the box is open.
    public enum Location: Equatable, Sendable {
        case closed
        case thisMac
        case server(String)
    }

    public let box: String
    public private(set) var location: Location = .closed {
        didSet {
            // Back on this Mac (from a server, or opened): its saves are read
            // here again, so the inspector says "Loading saves…" until
            // portenvd answers, never "No saves yet." from before.
            if location == .thisMac && oldValue != .thisMac {
                saves = []
                savesLoaded = false
                savesFailed = false
            }
        }
    }
    public private(set) var state: BoxState?
    /// Opening or moving, which are not save states; nil otherwise.
    public private(set) var progress: String? = "Opening…"
    public private(set) var servers: [String] = []
    public private(set) var busy = false
    /// The last failure, shown in a sheet; cleared by dismissError.
    public private(set) var error: String?
    /// Something to tell the person once, in an alert with OK (the security
    /// restart, R-0201); cleared by dismissNotice.
    public private(set) var notice: String?
    private var securityRestartTold = false
    /// What the window says after portenvd restarted the workspace to apply
    /// a security update (R-0201).
    public static let securityRestartNotice = "Portenv restarted this workspace to apply a security update. Your files and saves are as you left them; programs that were running were stopped."
    /// The workspace is open somewhere else: the sheet that asks what to do
    /// (2.3, the move-and-lease board).
    public private(set) var leaseSheet: LeaseSheet?
    /// Move To asks first: what will stop, and where it goes.
    public private(set) var moveConfirmation: MoveConfirmation?
    /// Keep Waiting: the place being waited for, while the open is tried
    /// again.
    public private(set) var waitingFor: String?
    /// How often Keep Waiting tries the open again.
    public var keepWaitingEvery: Duration = .seconds(30)
    private var waiting: Task<Void, Never>?
    /// Changes whenever the box starts again (here or on a server), so the
    /// terminal reattaches to the new start.
    public private(set) var terminalGeneration = 0
    /// The box's saves, for the inspector (§6); empty for a box on a server
    /// (listed there) or before the first read.
    public private(set) var saves: [SaveInfo] = []
    /// Set once portenvd has answered for the saves, so an empty list isn't
    /// shown as "No saves yet." while it's still loading (R-0054).
    public private(set) var savesLoaded = false
    /// The last read of the saves failed: the inspector says so, with Try
    /// Again, instead of "Loading saves…" forever.
    public private(set) var savesFailed = false
    /// How long keepSavesLoaded waits between quiet retries.
    private let savesRetry: Duration
    /// When the saved-time words were last brought up to date. "Saved just
    /// now" becomes "Saved at <time>" a minute after the save with no new
    /// state (§3.1); views read this so they redraw then (R-0066: a
    /// TimelineView kept its first schedule and never rolled over).
    public private(set) var savedTimeNow: Date
    private var savedTimeTicker: Task<Void, Never>?
    /// Re-reads the saves when a new save is recorded (R-0066: the list
    /// once lagged the state line by a minute).
    private var savesReload: Task<Void, Never>?
    private let sleep: @Sendable (Duration) async -> Void
    /// Where a move in progress is going (the inspector shows the move).
    public private(set) var movingTo: String?

    private let daemon: DaemonAPI
    /// The window's tab terminals (ADR 0020): one stream per tab through
    /// portenvd, when the daemon client can open them.
    @ObservationIgnored public let tabTerminals: (any TabTerminalProvider)?
    private let keychain: KeychainReading
    private let notifier: KeychainWaitNotifying?
    private let keychainThreshold: Duration
    /// Makes sure launchd runs portenvd (the login item); returns a plain
    /// problem to show (for example, Login Items needs the person's
    /// approval), or nil. The app never starts portenvd itself.
    private let ensureDaemon: (@Sendable () async -> String?)?
    /// How long portenvd may take to answer at launch before the window
    /// says so plainly (waitForDaemon).
    private let daemonDeadline: Duration
    private var daemonGoneSince: SuspendingClock.Instant?
    /// Time awake: a Mac asleep while portenvd is away mustn't spend the
    /// deadline (a wall clock did, on 2026-10-16).
    private let awake: @Sendable () -> SuspendingClock.Instant

    /// What the window says when portenvd doesn't answer at launch.
    public static let daemonGone = "Portenv's background service isn't running, so nothing is being saved. It's started again automatically; if this lasts, quit and reopen Portenv."
    /// Reopening after portenvd restarted: the state line keeps the real
    /// state instead of "Opening…".
    private var resuming = false
    /// portenvd went away while this window had the workspace open here:
    /// whatever the new portenvd says first (interrupted, or closed), the
    /// window reopens it by itself, never showing "closed" (R-0250).
    private var lostWhileOpenHere = false
    /// portenvd is away, or the workspace is being reopened after it came
    /// back: the tab view says it's reconnecting, never "disconnected" with
    /// a Reconnect that can't work yet (R-0242).
    public var reconnectingToService: Bool { daemonGoneSince != nil || resuming }
    /// One reopen per restart, so a failing one doesn't read the Keychain
    /// every few seconds.
    private var resumeTried = false
    /// Portenv is quitting to relaunch for an update: portenvd was asked to
    /// leave, and no new one may start before the app quits.
    private var leaving = false
    /// How long follow waits before watching again after portenvd went.
    private let followRetry: Duration
    /// The time, for how long a move took (tests replace it).
    private let now: @Sendable () -> Date

    /// macOS is (or may be) asking the person to approve a Keychain read:
    /// the window says so and offers Cancel.
    public private(set) var waitingForKeychain = false
    /// A secondary line under a progress line (the Keychain wait's hint).
    public private(set) var progressDetail: String?
    private var cancelKeychain: (@MainActor () -> Void)?
    /// Brings this window forward before a read that may prompt (R-0214).
    private let bringForward: @Sendable () -> Void

    public init(box: String, daemon: DaemonAPI, keychain: KeychainReading = SystemKeychain(),
                notifier: KeychainWaitNotifying? = nil, keychainThreshold: Duration = KeychainWait.threshold,
                ensureDaemon: (@Sendable () async -> String?)? = nil, followRetry: Duration = .seconds(1),
                daemonDeadline: Duration = .seconds(30), savesRetry: Duration = .seconds(5),
                awake: @escaping @Sendable () -> SuspendingClock.Instant = { .now },
                now: @escaping @Sendable () -> Date = { Date() },
                sleep: @escaping @Sendable (Duration) async -> Void = { try? await Task.sleep(for: $0) },
                bringForward: @escaping @Sendable () -> Void = { KeychainWait.front.bringForward() }) {
        self.box = box
        self.bringForward = bringForward
        self.savesRetry = savesRetry
        self.now = now
        self.sleep = sleep
        // Set from the clock when a save is first recorded (savedAtChanged).
        self.savedTimeNow = .distantPast
        self.daemon = daemon
        tabTerminals = (daemon as? any TabTerminalTransport).map { ControlModeTabProvider(box: box, transport: $0) }
        self.keychain = keychain
        self.notifier = notifier
        self.keychainThreshold = keychainThreshold
        self.ensureDaemon = ensureDaemon
        self.daemonDeadline = daemonDeadline
        self.awake = awake
        self.followRetry = followRetry
    }

    /// A message shown for StateLine.transientSeconds (a revert, a move
    /// finished), then the normal line again.
    public private(set) var transient: String?
    /// What the state line shows for the link under the pointer in the
    /// terminal, or one it just refused (TerminalLinks.status): where it
    /// goes before anyone clicks, or why it won't open (R-0134, R-0142).
    /// Never the raw link.
    public var linkStatus: String?
    private var transientTask: Task<Void, Never>?

    /// The title's state line: progress while opening or moving, a transient
    /// message, otherwise the recorded save state only.
    public var subtitle: String { subtitle(at: now()) }

    /// The state line as it reads at a time: the title draws it at the time
    /// it's drawn (and again when savedAtChangesAt comes).
    public func subtitle(at time: Date) -> String {
        progress ?? transient ?? (engineDown ? DockerText.stateLine : state?.line(now: time)) ?? ""
    }

    /// Docker Desktop stopped while the box is open on this Mac (R-0098):
    /// the state line and the window say so, with Open Docker Desktop.
    public private(set) var engineDown = false

    /// Asks portenvd whether Docker answers, only while the box is open
    /// here and its agent is unavailable (a working box never asks).
    public func checkEngine() async {
        guard isOpen, location == .thisMac, agentUnavailable else {
            engineDown = false
            return
        }
        engineDown = await daemon.engineRunning() == false
    }

    /// checkEngine every 2 s until cancelled (alongside follow).
    public func watchEngine() async {
        while !Task.isCancelled {
            await checkEngine()
            await sleep(DockerText.interval)
        }
    }

    /// A new save was recorded: the words roll over a minute later, and the
    /// saves list (once read) is read again now, not on a later state.
    private func savedAtChanged() {
        savedTimeNow = now()
        savedTimeTicker?.cancel()
        savedTimeTicker = Task { [weak self] in await self?.keepSavedTimeCurrent() }
        if savesLoaded && location == .thisMac {
            savesReload?.cancel()
            savesReload = Task { [weak self] in await self?.keepSavesLoaded() }
        }
    }

    /// Brings savedTimeNow up to date when "just now" runs out for the save
    /// shown; returns once the words no longer change or when cancelled.
    func keepSavedTimeCurrent() async {
        while let next = savedAtChangesAt(now()) {
            await sleep(.seconds(max(0, next.timeIntervalSince(now()))))
            if Task.isCancelled { return }
            savedTimeNow = now()
        }
    }

    /// When the state line's "Saved just now" becomes "Saved at <time>", or
    /// nil (GUIDELINES §3.1).
    public func savedAtChangesAt(_ time: Date) -> Date? {
        state?.save == .saved ? SavedTime.nextChange(state?.savedAt, now: time) : nil
    }

    /// The title's look from one snapshot: the state line, the symbol and
    /// whether it spins always agree (§3.1).
    public var title: TitleLook { TitleLook(line: subtitle, symbol: symbol, spins: symbolSpins) }

    /// The sync symbol next to the title (§3.1).
    public var symbol: String? {
        if waitingForKeychain { return "lock" }
        return progress != nil ? "arrow.triangle.2.circlepath" : transient != nil ? "checkmark.circle" : state?.symbol
    }

    /// Whether the symbol animates (the view turns it off with Reduce Motion).
    public var symbolSpins: Bool {
        !waitingForKeychain && (progress != nil || (transient == nil && state?.symbolSpins == true))
    }

    /// The box's menu (§3.2), for the title menu and the menu bar's Box menu.
    public func menuItems(optionHeld: Bool) -> [BoxMenuItem] {
        let here: String? = switch location {
        case .thisMac: "this-mac"
        case .server(let s): s
        case .closed: nil
        }
        return BoxMenuModel.items(open: isOpen, busy: busy, agentUnavailable: agentUnavailable,
                                  optionHeld: optionHeld, location: here, servers: servers,
                                  lastSavePoint: lastSavePoint, savesKnown: savesLoaded && location == .thisMac, now: now(),
                                  agentsRunning: agentsRunning)
    }

    /// The newest save point among the saves read here, if any.
    public var lastSavePoint: Date? {
        saves.filter { $0.kind == "point" }.map(\.time).max()
    }

    /// Move To ▸'s places, for the inspector's Move To… button (the same as
    /// the title menu's).
    public var moveMenu: [BoxMenuItem] {
        let here: String? = switch location {
        case .thisMac: "this-mac"
        case .server(let s): s
        case .closed: nil
        }
        return BoxMenuModel.moveSubmenu(busy: busy, location: here, servers: servers)
    }

    /// Delete Workspace… was chosen: the app shows its sheet (ADR 0029).
    @ObservationIgnored public var onDelete: ((String) -> Void)?

    /// An agent of the workspace's team is running (R-0358): Delete
    /// Workspace… is disabled.
    public var agentsRunning = false

    /// Runs a menu item's action.
    public func run(_ action: BoxMenuItem.Action) async {
        switch action {
        case .none: break
        case .open: await open()
        case .move(let target): await requestMove(to: target)
        case .revertToLastSavePoint: await revertToLastSavePoint()
        case .makeSavePoint: await makeSavePoint()
        case .restartBox: await restartBox()
        case .deleteWorkspace: onDelete?(box)
        }
    }

    private func show(_ message: String) {
        transient = message
        transientTask?.cancel()
        transientTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(StateLine.transientSeconds))
            guard !Task.isCancelled else { return }
            self?.transient = nil
        }
    }

    /// The box agent stopped answering: nothing can be saved, and unsaved
    /// changes stay in the box until Restart Box.
    public var agentUnavailable: Bool { state?.save == .agentUnavailable }

    /// The box is open somewhere this window can act on.
    public var isOpen: Bool { location != .closed }

    /// Whether a Move To target is where the box runs (shown checked).
    public func isCurrent(_ target: String) -> Bool {
        switch location {
        case .thisMac: return target == "this-mac"
        case .server(let s): return target == s
        case .closed: return false
        }
    }

    /// Why the last open failed, in portenvd's plain words (DaemonErrorText):
    /// the closed window shows it with Try Again, instead of an alert alone
    /// or a silent "is closed" (R-0211). Cleared when an open starts.
    public private(set) var openFailure: String?

    /// Opens the box (on this Mac, or shows it where it runs).
    public func open() async {
        stopWaiting() // a new open ends Keep Waiting (R-0178)
        openFailure = nil
        progress = resuming ? nil : "Opening…"
        var elsewhere = false
        await perform(failure: { [weak self] in self?.openFailure = $0 }) {
            self.servers = (try? await self.daemon.servers()) ?? []
            do {
                try await self.daemon.open(self.box)
            } catch let e as DaemonError where e.openElsewhere {
                // Open somewhere else: the sheet, not an error line.
                elsewhere = true
                return
            } catch let e as DaemonError where e.message.contains(KeychainWait.daemonError) {
                // portenvd never prompts: read the keys here, in front of
                // the person, and hand them over. Cancelled: nothing opened.
                guard try await self.approveKeys() else {
                    self.location = .closed
                    self.openFailure = KeychainWait.cancelled
                    return
                }
                try await self.daemon.open(self.box)
            }
            self.terminalGeneration += 1
            // Learn where it runs before the busy state ends, so the window
            // never shows "is closed" between the open and the terminal.
            await self.refresh()
        }
        progress = nil
        progressDetail = nil
        if elsewhere {
            await showWhereItIsOpen()
            return
        }
        if location == .closed { await refresh() }
    }

    // MARK: Open somewhere else, and Move To (2.3)

    /// The sheet at once, naming the place; then the probe (up to 2 s)
    /// fills it in (R-0140). Never an error line.
    private func showWhereItIsOpen(retried: Bool = false) async {
        guard let first = try? await daemon.placement(box, probe: false) else {
            openFailure = "\(box) is open somewhere else."
            return
        }
        // The other place let go meanwhile: open it once more (R-0178).
        if first.holder == .none {
            await openOnceMore(retried: retried)
            return
        }
        leaseSheet = LeaseSheet(box: box, placement: first, now: now())
        guard case .knownServer = first.holder else { return }
        if let probed = try? await daemon.placement(box, probe: true), leaseSheet != nil {
            if probed.holder == .none {
                leaseSheet = nil
                await openOnceMore(retried: retried)
                return
            }
            leaseSheet = LeaseSheet(box: box, placement: probed, now: now())
        }
    }

    /// One more open after the other place let go; refused again, the
    /// sheet as usual (never a second retry).
    private func openOnceMore(retried: Bool) async {
        guard !retried else {
            openFailure = "\(box) is open somewhere else."
            return
        }
        var elsewhere = false
        await perform {
            do {
                try await self.daemon.open(self.box)
            } catch let e as DaemonError where e.openElsewhere {
                elsewhere = true
                return
            }
            self.terminalGeneration += 1
            await self.refresh()
        }
        if elsewhere { await showWhereItIsOpen(retried: true) }
    }

    /// Stop Waiting, or anything else that ends the wait: no further open
    /// is tried (R-0178).
    public func stopWaiting() {
        waiting?.cancel()
        waiting = nil
        if waitingFor != nil {
            waitingFor = nil
            progress = nil
        }
    }

    /// What the person chose on the sheet.
    public func choose(_ action: LeaseSheet.Action) async {
        let placeName = leaseSheet?.place ?? ""
        leaseSheet = nil
        switch action {
        case .connect:
            progress = "Opening…"
            await perform {
                try await self.daemon.connectThere(self.box)
                self.terminalGeneration += 1
                await self.refresh()
            }
            progress = nil
        case .moveHere:
            await move(to: "this-mac")
        case .moveHereTakingOver:
            progress = "Opening…"
            await perform {
                try await self.daemon.open(self.box, takeOver: true)
                self.terminalGeneration += 1
                await self.refresh()
            }
            progress = nil
        case .keepWaiting:
            keepWaiting(for: placeName)
        }
    }

    public func cancelLeaseSheet() { leaseSheet = nil }

    /// Keep Waiting: try the open again every keepWaitingEvery until it
    /// works (the other place let go), or until the place answers: then
    /// the sheet comes back, now offering Connect (the board's "tells you
    /// when it answers").
    private func keepWaiting(for place: String) {
        waiting?.cancel()
        waitingFor = place
        progress = "Waiting for \(place)\u{2026}"
        waiting = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                try? await Task.sleep(for: self.keepWaitingEvery)
                if Task.isCancelled { return }
                do {
                    try await self.daemon.open(self.box)
                    self.waitingFor = nil
                    self.progress = nil
                    self.terminalGeneration += 1
                    await self.refresh()
                    return
                } catch let e as DaemonError where e.openElsewhere {
                    if let p = try? await self.daemon.placement(self.box, probe: true), p.answers == .yes {
                        self.waitingFor = nil
                        self.progress = nil
                        self.leaseSheet = LeaseSheet(box: self.box, placement: p, now: self.now())
                        return
                    }
                    continue
                } catch {
                    // Anything else (the Keychain, the server removed) ends
                    // the wait and is said (R-0178).
                    if Task.isCancelled { return }
                    self.waitingFor = nil
                    self.progress = nil
                    // A failed open: in the window, with Try Again (R-0211).
                    self.openFailure = error.localizedDescription
                    return
                }
            }
        }
    }

    /// Move To asks first, naming every command that will stop.
    public func requestMove(to target: String) async {
        guard !isCurrent(target) else { return }
        stopWaiting()
        // nil when they can't be read: then nothing is claimed (R-0178).
        let tabs = try? await daemon.tabs(box)
        moveConfirmation = MoveConfirmation(box: box, target: target, tabs: tabs, now: now())
    }

    public func confirmMove() async {
        guard let m = moveConfirmation else { return }
        moveConfirmation = nil
        await move(to: m.target)
    }

    public func cancelMove() { moveConfirmation = nil }

    /// portenvd isn't answering while the box is open (it crashed, is being
    /// updated or relaunched, or is frozen): the line shows the box's real
    /// state, "Not saved since" its last save, while launchd starts portenvd
    /// again. When the login item needs approval, the window says so
    /// plainly. Once portenvd is back, the box is reopened.
    ///
    /// No alert past a deadline (R-0294): the tab's terminal says it, in
    /// place, with the capsule and then the Disconnected card, whose line
    /// says nothing is being saved (TabTerminalShown.serviceAwayLine).
    private func daemonEnded() async {
        guard !leaving, location != .closed, !(await daemon.ping()) else { return }
        state = BoxState(save: .notSaved, savedAt: state?.savedAt, location: state?.location)
        if daemonGoneSince == nil { daemonGoneSince = awake() }
        if let problem = await ensureDaemon?() {
            error = problem
        }
    }

    /// Waits for portenvd at launch (launchd starts it with the login item),
    /// saying so plainly if it doesn't come within the deadline.
    public func waitForDaemon() async {
        let start = awake()
        while !(await daemon.ping()) {
            if let problem = await ensureDaemon?() {
                error = problem
            } else if awake() - start >= daemonDeadline {
                error = Self.daemonGone
                return
            }
            try? await Task.sleep(for: .milliseconds(200))
        }
        if error == Self.daemonGone || error == nil { error = nil }
    }

    /// At launch: one notification for each box saved in the background
    /// after Quit Anyway since Portenv last ran (PLAN.md 1.1; at once from
    /// portenvd itself is a 1.8 note).
    public func announceBackgroundSaves() async {
        guard let saved = try? await daemon.takeSavedAfterQuit() else { return }
        for s in saved {
            await notifier?.savedAfterQuit(box: s.box, at: s.savedAt)
        }
    }

    /// Called before portenvd is asked to relaunch (an update): the app
    /// stops reacting to portenvd's absence while it quits; launchd starts
    /// the next portenvd, which takes the box over.
    public func relaunchingForUpdate() { leaving = true }

    private func resume() async {
        resuming = true
        defer { resuming = false }
        await open()
    }

    /// Cancel while macOS asks to approve the Keychain read: the open stops
    /// before portenvd starts anything (no box, no lease). The read itself
    /// ends whenever the prompt is answered; its result is dropped.
    public func cancelKeychainWait() { cancelKeychain?() }

    /// Reads the box's keys from the Keychain off the main thread and hands
    /// them to portenvd. After keychainThreshold the window says it's
    /// waiting (and a notification goes out); no timeout while the person
    /// might be answering. Returns false when cancelled.
    private func approveKeys() async throws -> Bool {
        let ids = try await daemon.keyIDs(box)
        // Its prompt comes up over this window (R-0214).
        bringForward()
        let reader = keychain
        let once = Once<Result<[String: Data], Error>?>()
        let result = await withCheckedContinuation { (done: CheckedContinuation<Result<[String: Data], Error>?, Never>) in
            once.set { done.resume(returning: $0) }
            cancelKeychain = { once.resume(nil) }
            // On a queue of its own, never the cooperative pool: a read can
            // block for as long as the prompt is up, and blocked pool
            // threads starve every other task (CI's three cores hung).
            KeychainWait.queue.async {
                do {
                    var keys: [String: Data] = [:]
                    for id in ids {
                        if let key = try reader.read(account: id) { keys[id] = key }
                    }
                    once.resume(.success(keys))
                } catch {
                    once.resume(.failure(error))
                }
            }
            Task { @MainActor [keychainThreshold, notifier, box] in
                try? await Task.sleep(for: keychainThreshold)
                guard !once.isDone else { return }
                self.waitingForKeychain = true
                self.progress = KeychainWait.line
                self.progressDetail = KeychainWait.detail
                await notifier?.keychainWaiting(box: box)
            }
        }
        waitingForKeychain = false
        progress = resuming ? nil : "Opening…"
        progressDetail = nil
        cancelKeychain = nil
        guard let result else { return false }
        let keys = try result.get()
        try await daemon.provideKeys(box, keys)
        return true
    }

    /// Reads the recorded state from portenvd.
    public func refresh() async {
        guard let st = try? await daemon.state(box) else {
            await daemonEnded()
            return
        }
        await apply(st, pushed: false)
    }

    /// Follows the box's state as portenvd pushes it (WatchBoxState), in
    /// place of a poll. When the stream ends, portenvd has gone: the line
    /// shows the real state and portenvd is started again (item 1g); then
    /// the box is followed again. Runs until the task is cancelled.
    public func follow() async {
        while !Task.isCancelled {
            do {
                for try await st in daemon.watch(box) {
                    await apply(st, pushed: true)
                }
            } catch {}
            if Task.isCancelled { return }
            // The stream ended: portenvd went away. A workspace this window
            // had open here is reattached when it's back (R-0250).
            if location == .thisMac { lostWhileOpenHere = true }
            await daemonEnded()
            try? await Task.sleep(for: followRetry)
        }
    }

    /// Applies a state from portenvd. A pushed state, while an action runs,
    /// only updates the line: the action itself sets where the box is
    /// (refreshing before its busy state ends).
    private func apply(_ st: BoxState, pushed: Bool) async {
        let newSave = st.savedAt != state?.savedAt
        state = st
        // Once for this window, however often the state says it again.
        if st.restartedForSecurityUpdate && !securityRestartTold {
            securityRestartTold = true
            notice = Self.securityRestartNotice
        }
        if newSave { savedAtChanged() }
        // The first open's pull: the line says how far it has got, and
        // goes back to "Opening…" when it's done (R-0211).
        if busy, let line = progress, line == StateLine.opening || line.hasPrefix(StateLine.gettingImagePrefix) {
            progress = st.imagePull.map { StateLine.gettingImage(done: $0.done, total: $0.total) } ?? StateLine.opening
        }
        // portenvd answers again: whatever said it was away no longer holds.
        daemonGoneSince = nil
        if error == Self.daemonGone { error = nil }
        if pushed && busy { return }
        if st.interrupted || (lostWhileOpenHere && st.save == .closed) {
            // portenvd restarted while this window had the box open: open it
            // again, handing the keys over (the window stays on the box).
            if location != .closed && !busy && !resumeTried {
                resumeTried = true
                lostWhileOpenHere = false
                await resume()
            }
            return
        }
        resumeTried = false
        lostWhileOpenHere = false
        switch st.save {
        case .closed: location = .closed
        default: location = st.location.map { .server($0) } ?? .thisMac
        }
    }

    /// File › Make Save Point (⌘S).
    public func makeSavePoint() async {
        guard isOpen else { return }
        await perform { try await self.daemon.makeSavePoint(self.box) }
        await refresh()
    }

    /// Revert To ▸ Last Save Point, wherever the box runs. The home as it
    /// was is saved first.
    public func revertToLastSavePoint() async {
        guard isOpen else { return }
        await perform {
            // Say which save point, in local time, for a few seconds.
            if let t = try await self.daemon.revert(self.box) { self.show(StateLine.reverted(to: t)) }
        }
        await refresh()
    }

    /// Move To ▸ This Mac or a server. The current location is a no-op.
    public func move(to target: String) async {
        guard !isCurrent(target) else { return }
        progress = StateLine.moving(to: target)
        movingTo = target
        defer { movingTo = nil }
        let started = now()
        await perform {
            try await self.daemon.move(self.box, to: target)
            self.terminalGeneration += 1
            self.show(StateLine.moved(to: target))
            await self.refresh()
            let seconds = Int(self.now().timeIntervalSince(started).rounded())
            await self.notifier?.moveFinished(box: self.box, to: ServerName.display(target), seconds: seconds)
        }
        progress = nil
        if error != nil { await refresh() } // a failed move: where is it now
    }

    /// Retry in the inspector: the box agent tries the packages that
    /// couldn't be installed again now; the state shows the outcome.
    public func retryPackages() async {
        await perform { try await self.daemon.retryPackages(self.box) }
        await refresh()
    }

    /// Reads the box's saves for the inspector. A box on a server lists its
    /// saves there, so they aren't read through this Mac's portenvd.
    public func refreshSaves() async {
        guard location == .thisMac else { saves = []; savesLoaded = true; savesFailed = false; return }
        do {
            saves = try await daemon.saves(box)
            savesLoaded = true
            savesFailed = false
        } catch {
            savesFailed = true
        }
    }

    /// Reads the saves, and while that fails keeps trying quietly (the
    /// inspector says "Couldn't load saves." meanwhile), until it works or
    /// the task is cancelled.
    public func keepSavesLoaded() async {
        while !Task.isCancelled {
            await refreshSaves()
            if !savesFailed { return }
            try? await Task.sleep(for: savesRetry)
        }
    }

    /// Closing the window saves and releases a box open on this Mac.
    public func close() async {
        stopWaiting()
        guard location == .thisMac else { return }
        await perform { try await self.daemon.close(self.box) }
        location = .closed
    }

    /// The state line while closing the last box window saves the box
    /// (StateLine.closing), with the box's menu disabled; off again when
    /// the person chose to stay in the box.
    public func showClosing(_ on: Bool) {
        if on {
            progress = StateLine.closing
            busy = true
        } else if progress == StateLine.closing {
            progress = nil
            busy = false
        }
    }

    public func dismissError() { error = nil }

    public func dismissNotice() { notice = nil }

    /// What quitting needs: this box, and whether it is open on this Mac.
    public var quitFlow: QuitFlow { QuitFlow(daemon: daemon, box: box, openHere: location == .thisMac) }

    /// The Mac woke from sleep: portenvd checks the box agent's channel and
    /// restarts a box whose channel is gone; reconnect the terminal then.
    public func woke() async {
        guard isOpen else { return }
        if (try? await daemon.woke()) == true {
            terminalGeneration += 1
        }
        await refresh()
    }

    /// The terminal's connection ended without the user leaving it: ask
    /// portenvd whether the box agent still answers; reattach if it does.
    public func terminalEnded() async {
        guard isOpen, !busy else { return }
        if (try? await daemon.check(box)) == true {
            terminalGeneration += 1
        }
        await refresh()
    }

    /// Restart Box: stops the box and opens it again where it runs; the home
    /// and its unsaved changes stay (the resume rules never restore over
    /// them).
    public func restartBox() async {
        guard isOpen else { return }
        await perform {
            try await self.daemon.restart(self.box)
            self.terminalGeneration += 1
        }
        await refresh()
    }

    /// Runs an action while busy. A failure goes to `failure` when given
    /// (an open's failure stays in the window), otherwise to the alert.
    private func perform(failure: ((String) -> Void)? = nil, _ action: @escaping @MainActor () async throws -> Void) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            try await action()
        } catch {
            if let failure { failure(error.localizedDescription) } else { self.error = error.localizedDescription }
        }
    }
}
