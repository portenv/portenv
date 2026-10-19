// SPDX-License-Identifier: Apache-2.0

import Observation

/// The tab a terminal view shows, and how its stream is doing (ADR 0020):
/// the main window's, which follows the tab bar's current tab.
///
/// - `show(tab:generation:)` opens the tab's stream, closing the one before;
///   the same tab and generation again does nothing.
/// - A lost stream is `.reconnecting` until its next reset, and
///   `.disconnected` (with why, and Reconnect) when it gives up.
/// - Trouble that lasts past `deadline` (time awake) shows Disconnected
///   whatever else holds, even while portenvd is said to be coming back
///   (R-0294). Only a stream that stays up for `settle` after its reset
///   ends the trouble: a reconnect blip shorter than that doesn't start
///   the clock over.
/// - Until the stream's first reset the tab is `.opening` ("Opening
///   <tab>…"), and a stream that fails before then is `.couldNotOpen`, with
///   Try Again: never a bare background (basics audit, R-0384). An open
///   that sends nothing at all (no reset, no end) for `openDeadline` is
///   `.couldNotOpen` too (R-0386).
@MainActor
@Observable
public final class TabTerminalShown {
    /// What the view says while a lost stream comes back (R-0242).
    public static let reconnectingLine = "Reconnecting to Portenv\u{2019}s background service\u{2026}"

    /// Whether to show the reconnecting line rather than Disconnected:
    /// while the stream retries, and while portenvd is away or the
    /// workspace is being reopened (`serviceComing`), whatever the stream
    /// said last.
    public func showsReconnecting(serviceComing: Bool) -> Bool {
        overlay(serviceComing: serviceComing) == .reconnecting
    }

    /// What the view shows over the terminal.
    public enum Overlay: Equatable, Sendable {
        case none
        /// The capsule: "Reconnecting to Portenv's background service…".
        case reconnecting
        /// The card: "Disconnected", why, and Reconnect.
        case disconnected(String?)
        /// In place of the terminal, before the tab's first reset:
        /// "Opening <tab>…" with a small spinner.
        case opening
        /// In place of the terminal, when it never opened: "Couldn't open
        /// <tab>", why, and Try Again.
        case couldNotOpen(String)
    }

    /// What the pane says while the tab opens.
    public static func openingLine(tab: String) -> String {
        "Opening \(tab.isEmpty ? "tab" : tab)\u{2026}"
    }

    /// The card's title when the tab's terminal didn't open.
    public static func couldNotOpenTitle(tab: String) -> String {
        "Couldn\u{2019}t open \(tab.isEmpty ? "the tab" : tab)"
    }

    /// The card's button: opens the tab's stream again.
    public static let tryAgain = "Try Again"

    /// Said on the card when the tab never opened and nothing better is known.
    public static let notOpeningLine = "This tab\u{2019}s terminal didn\u{2019}t open. The tab keeps running in your workspace; click Try Again."

    /// Said on the card when portenvd is away (R-0294): never a modal on
    /// top (GUIDELINES.md), and it says nothing is being saved.
    public static let serviceAwayLine = "Portenv\u{2019}s background service isn\u{2019}t answering, so nothing is being saved. It\u{2019}s started again automatically; if this lasts, quit and reopen Portenv."
    /// Said on the card when the trouble lasts and nothing better is known.
    public static let notAnsweringLine = "This tab\u{2019}s terminal isn\u{2019}t answering. Portenv keeps trying; click Reconnect to try now."

    public func overlay(serviceComing: Bool) -> Overlay {
        if overdue || (!opened && openTimedOut) {
            if serviceComing { return opened ? .disconnected(Self.serviceAwayLine) : .couldNotOpen(Self.serviceAwayLine) }
            if case let .disconnected(why) = connection, let why { return opened ? .disconnected(why) : .couldNotOpen(why) }
            return opened ? .disconnected(failure ?? Self.notAnsweringLine) : .couldNotOpen(failure ?? Self.notOpeningLine)
        }
        if !opened {
            if case let .disconnected(why) = connection, !serviceComing { return .couldNotOpen(why ?? Self.notOpeningLine) }
            return .opening
        }
        switch connection {
        case .connecting, .live: return .none
        case .reconnecting: return .reconnecting
        case let .disconnected(why): return serviceComing ? .reconnecting : .disconnected(why)
        }
    }

    /// How long trouble may last before the card shows.
    public static let deadline: Duration = .seconds(25)
    /// How long an open may send nothing at all before the card shows.
    public static let openDeadline: Duration = .seconds(20)
    /// How long a stream must stay up after its reset to end the trouble.
    public static let settle: Duration = .seconds(2)

    public enum Connection: Equatable, Sendable {
        case connecting, live, reconnecting
        case disconnected(String?)
    }

    public private(set) var source: (any TabTerminalSource)?
    public private(set) var connection = Connection.connecting
    /// The size the last reset gave: the next stream opens at it.
    public private(set) var size = TabTerminalSize(cols: 80, rows: 24)
    /// The trouble has lasted past the deadline: the card shows.
    public private(set) var overdue = false
    /// The shown tab's stream has reset at least once since it was shown:
    /// there's something on screen. Reconnect keeps it; another tab or a
    /// new generation clears it.
    public private(set) var opened = false
    /// The open sent nothing for `openDeadline`: the card shows.
    public private(set) var openTimedOut = false
    @ObservationIgnored private let openDeadline: Duration
    @ObservationIgnored private var openTimer: Task<Void, Never>?
    @ObservationIgnored private let provider: any TabTerminalProvider
    @ObservationIgnored private var shown: (tab: String, generation: Int)?
    @ObservationIgnored private let deadline: Duration
    @ObservationIgnored private let settle: Duration
    /// The stream is in trouble and hasn't settled since (tests read it).
    @ObservationIgnored private(set) var inTrouble = false
    @ObservationIgnored private var overdueTimer: Task<Void, Never>?
    @ObservationIgnored private var settling: Task<Void, Never>?
    /// Waits time awake: SuspendingClock, so time asleep doesn't count
    /// (the 2026-10-16 sleep flake). Tests replace it.
    @ObservationIgnored private let sleep: @Sendable (Duration) async -> Void

    public init(provider: any TabTerminalProvider, deadline: Duration = TabTerminalShown.deadline,
                settle: Duration = TabTerminalShown.settle,
                openDeadline: Duration = TabTerminalShown.openDeadline,
                sleep: @escaping @Sendable (Duration) async -> Void = { try? await Task.sleep(for: $0, clock: .suspending) }) {
        self.provider = provider
        self.deadline = deadline
        self.settle = settle
        self.openDeadline = openDeadline
        self.sleep = sleep
    }

    /// Why the stream itself last failed, in plain words.
    private var failure: String? {
        guard let why = (source as? ControlModeTabSource)?.failure else { return nil }
        return DaemonErrorText.isTransport(why) ? nil : why
    }

    public func show(tab: String, generation: Int) {
        if let shown, shown.tab == tab, shown.generation == generation, source != nil { return }
        shown = (tab, generation)
        opened = false
        open()
    }

    /// Reconnect: the shown tab's stream again. The person asked, so the
    /// card goes and the deadline starts again.
    public func reconnect() {
        clearTrouble()
        open()
        troubleBegan()
    }

    public func stop() {
        source?.close()
        source = nil
        shown = nil
        opened = false
        stopOpenTimer()
        clearTrouble()
    }

    public func reset(_ size: TabTerminalSize) {
        self.size = size
        connection = .live
        opened = true
        stopOpenTimer()
        guard inTrouble, settling == nil else { return }
        settling = Task { [weak self, settle, sleep] in
            await sleep(settle)
            guard !Task.isCancelled, let self else { return }
            self.settling = nil
            if self.connection == .live { self.clearTrouble() }
        }
    }

    public func ended(_ why: TabTerminalEnd) {
        // Something came: the trouble deadline takes over from here.
        if case .lost = why { openTimer?.cancel(); openTimer = nil }
        switch why {
        case .lost(retrying: true):
            connection = .reconnecting
            troubleBegan()
        case .lost(retrying: false):
            connection = .disconnected(failure)
            troubleBegan()
        case .tabClosed, .boxStopped:
            // The tab bar moves to another tab, or the window shows the
            // stopped workspace: either replaces this stream.
            break
        }
    }

    /// The stream is in trouble: the clock starts, unless it's already
    /// running (a blip doesn't start it over).
    private func troubleBegan() {
        settling?.cancel()
        settling = nil
        guard !inTrouble else { return }
        inTrouble = true
        overdueTimer = Task { [weak self, deadline, sleep] in
            await sleep(deadline)
            guard !Task.isCancelled, let self, self.inTrouble else { return }
            self.overdue = true
        }
    }

    private func clearTrouble() {
        inTrouble = false
        overdue = false
        overdueTimer?.cancel()
        overdueTimer = nil
        settling?.cancel()
        settling = nil
    }

    private func open() {
        guard let shown else { return }
        source?.close()
        connection = .connecting
        source = provider.open(tab: shown.tab, size: size)
        stopOpenTimer()
        guard !opened else { return }
        openTimer = Task { [weak self, openDeadline, sleep] in
            await sleep(openDeadline)
            guard !Task.isCancelled, let self, !self.opened else { return }
            self.openTimer = nil
            self.openTimedOut = true
        }
    }

    private func stopOpenTimer() {
        openTimer?.cancel()
        openTimer = nil
        openTimedOut = false
    }
}
