// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The control-mode provider (ADR 0020): each tab's terminal is its own
/// stream from portenvd's TabTerminal, which relays the workspace helper's.
/// It reconnects by itself when the stream is lost, and every reconnect
/// starts with a new `reset`, so the view needs nothing it had kept.

/// One message from a tab's stream, as portenvd sends it.
public enum TabTerminalMessage: Equatable, Sendable {
    /// Start over. `skipped`: output was too fast to show in full, and the
    /// seed is the current screen.
    case reset(TabTerminalSize, seed: [UInt8], skipped: Bool)
    case output([UInt8])
    case resized(TabTerminalSize)
    /// Who ran a command, for its block (used by command blocks).
    case author(line: UInt64, name: String, isYou: Bool, agentID: String)
    /// Nothing follows: the tab closed, or the workspace is stopping.
    case tabClosed
    case boxStopping
}

/// What the app sends on a tab's stream after it opens.
public enum TabTerminalRequest: Equatable, Sendable {
    case input([UInt8])
    case resize(TabTerminalSize)
}

/// Why a stream couldn't be opened or went away. `permanent` failures (a
/// tmux too old for the app's terminal, a workspace that isn't open) end
/// the view's stream; others are tried again.
public struct TabTerminalStreamError: Error, Equatable, Sendable {
    public let message: String
    public let permanent: Bool
    public init(_ message: String, permanent: Bool) {
        self.message = message
        self.permanent = permanent
    }
}

/// One open stream: its messages, and a way to send on it and end it.
public struct TabTerminalConnection: Sendable {
    public let messages: AsyncThrowingStream<TabTerminalMessage, Error>
    public let send: @Sendable (TabTerminalRequest) -> Void
    public let close: @Sendable () -> Void
    public init(messages: AsyncThrowingStream<TabTerminalMessage, Error>,
                send: @escaping @Sendable (TabTerminalRequest) -> Void,
                close: @escaping @Sendable () -> Void) {
        self.messages = messages
        self.send = send
        self.close = close
    }
}

/// Opens tab streams through portenvd (DaemonClient), or a fake in tests.
public protocol TabTerminalTransport: Sendable {
    func tabTerminal(box: String, session: String, tab: String, size: TabTerminalSize) -> TabTerminalConnection
}

/// One workspace window's tab terminals (`controller.tabTerminals`).
@MainActor
public final class ControlModeTabProvider: TabTerminalProvider {
    public let box: String
    let transport: any TabTerminalTransport
    let retry: ControlModeTabSource.Retry

    public init(box: String, transport: any TabTerminalTransport, retry: ControlModeTabSource.Retry = .standard) {
        self.box = box
        self.transport = transport
        self.retry = retry
    }

    public func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource {
        ControlModeTabSource(box: box, tab: tab, size: size, transport: transport, retry: retry)
    }
}

/// One tab's terminal over portenvd's TabTerminal.
///
/// - Every stream starts with `reset`; after a lost stream the source says
///   `.lost(retrying: true)`, opens again at the size last asked for, and
///   the next `reset` redraws everything.
/// - After `Retry.attempts` failures in a row with no `reset` between, or
///   a permanent failure, it says `.lost(retrying: false)` and ends: the
///   view offers Reconnect, which opens a new source.
/// - A stream counts as working again only once it has stayed up for
///   `Retry.settle` after its reset: a portenvd killed again within a
///   second of each restart (R-0294) doesn't start the count over.
/// - A stream with no reset within `Retry.firstReset` is given up on and
///   counted as a failure: a frozen portenvd accepts the connection and
///   never answers (R-0294). A live stream's hang is gRPC keepalive's
///   (DaemonClient.Liveness).
/// - Input while it's reconnecting isn't sent: the view shows
///   "Reconnecting…", and nothing typed then reaches the tab later.
@MainActor
public final class ControlModeTabSource: TabTerminalSource {
    /// How it tries again: waits that double from `first` up to `longest`,
    /// at most `attempts` in a row. `firstReset`: how long an opened stream
    /// may go without its reset. `settle`: how long a stream must stay up
    /// after its reset to count as working again.
    public struct Retry: Sendable {
        public var first: Duration
        public var longest: Duration
        public var attempts: Int
        public var firstReset: Duration
        public var settle: Duration
        public init(first: Duration, longest: Duration, attempts: Int,
                    firstReset: Duration = .seconds(10), settle: Duration = .seconds(2)) {
            self.first = first
            self.longest = longest
            self.attempts = attempts
            self.firstReset = firstReset
            self.settle = settle
        }

        public static let standard = Retry(first: .milliseconds(250), longest: .seconds(4), attempts: 8)
    }

    /// The workspace's own tmux session, whose windows are the tabs.
    public static let session = "main"

    public let tab: String
    public let events: AsyncStream<TabTerminalEvent>
    /// Whether the last reset said output was skipped (the view's notice).
    public private(set) var lastResetSkipped = false
    /// Why the stream gave up, when it says so (shown with Reconnect).
    public private(set) var failure: String?
    /// The reason when a stream opened but never reset (a frozen portenvd).
    public static let notAnswering = "Portenv\u{2019}s background service isn\u{2019}t answering."
    let box: String
    private let continuation: AsyncStream<TabTerminalEvent>.Continuation
    private let transport: any TabTerminalTransport
    private let retry: Retry
    private var size: TabTerminalSize
    private var connection: TabTerminalConnection?
    private var reading: Task<Void, Never>?
    private var closed = false
    private var live = false // a reset came on the current connection
    private var liveAt: ContinuousClock.Instant? // when that reset came
    private var attempt = 0
    private var unanswered = false // the current attempt passed firstReset

    init(box: String, tab: String, size: TabTerminalSize, transport: any TabTerminalTransport, retry: Retry) {
        self.box = box
        self.tab = tab
        self.size = size
        self.transport = transport
        self.retry = retry
        let stream = AsyncStream.makeStream(of: TabTerminalEvent.self)
        events = stream.stream
        continuation = stream.continuation
        reading = Task { [weak self] in await self?.run() }
    }

    public func send(_ input: [UInt8]) {
        guard live, !input.isEmpty else { return }
        connection?.send(.input(input))
    }

    public func resize(_ size: TabTerminalSize) {
        guard size.cols > 0, size.rows > 0 else { return }
        self.size = size
        if live { connection?.send(.resize(size)) }
    }

    public func close() {
        guard !closed else { return }
        closed = true
        reading?.cancel()
        connection?.close()
        connection = nil
        continuation.finish()
    }

    private func finish(_ why: TabTerminalEnd) {
        guard !closed else { return }
        continuation.yield(.ended(why))
        close()
    }

    private func run() async {
        var failures = 0
        var wait = retry.first
        while !closed, !Task.isCancelled {
            let conn = transport.tabTerminal(box: box, session: Self.session, tab: tab, size: size)
            connection = conn
            live = false
            liveAt = nil
            attempt += 1
            unanswered = false
            let watchdog = Task { [weak self, mine = attempt, limit = retry.firstReset] in
                try? await Task.sleep(for: limit)
                guard !Task.isCancelled, let self, self.attempt == mine, !self.live, !self.closed else { return }
                self.unanswered = true
                conn.close()
            }
            var permanent = false
            do {
                for try await message in conn.messages {
                    if closed { return }
                    if apply(message) { return }
                }
            } catch let error as TabTerminalStreamError {
                permanent = error.permanent
                failure = error.message
            } catch {
                failure = error.localizedDescription
            }
            watchdog.cancel()
            if closed || Task.isCancelled { return }
            if unanswered {
                failure = Self.notAnswering
                permanent = false
            }
            conn.close()
            connection = nil
            if let liveAt, ContinuousClock.now - liveAt >= retry.settle {
                // It had worked, for long enough: start counting again.
                failures = 0
                wait = retry.first
            }
            failures += 1
            if permanent || failures > retry.attempts {
                finish(.lost(retrying: false))
                return
            }
            live = false
            continuation.yield(.ended(.lost(retrying: true)))
            try? await Task.sleep(for: wait)
            wait = min(wait * 2, retry.longest)
        }
    }

    /// Hands a message to the view; returns true when the stream is over.
    private func apply(_ message: TabTerminalMessage) -> Bool {
        switch message {
        case let .reset(size, seed, skipped):
            if !live { liveAt = ContinuousClock.now }
            live = true
            lastResetSkipped = skipped
            continuation.yield(.reset(size, seed: seed))
        case let .output(bytes):
            if live { continuation.yield(.output(bytes)) }
        case let .resized(size):
            if live { continuation.yield(.resized(size)) }
        case .author:
            break // command blocks take these
        case .tabClosed:
            finish(.tabClosed)
            return true
        case .boxStopping:
            finish(.boxStopped)
            return true
        }
        return false
    }
}
