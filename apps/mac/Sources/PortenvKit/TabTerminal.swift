// SPDX-License-Identifier: Apache-2.0

/// One tab's terminal stream (ADR 0020): what the app's terminal views and
/// split view's panes draw from, whoever provides it. Each view shows one
/// tab; two views of the same tab each open their own source.
///
/// The control-mode provider (portenvd → the box agent's control-mode
/// client) implements it; so does ScriptedTabSource, for tests and renders.
/// Bytes are the tab's own output: OSC 133 marks arrive in line with the
/// text they belong to.

/// A tab's size in cells.
public struct TabTerminalSize: Equatable, Sendable {
    public var cols: Int
    public var rows: Int

    public init(cols: Int, rows: Int) {
        self.cols = cols
        self.rows = rows
    }
}

/// Why a tab's stream ended.
public enum TabTerminalEnd: Equatable, Sendable {
    /// The tab was closed (by anyone).
    case tabClosed
    /// The workspace stopped, or moved away from this Mac.
    case boxStopped
    /// The connection to portenvd or the box agent went away. With
    /// `retrying`, the provider reconnects by itself and starts with a new
    /// `reset`; without, the view says so and offers to reconnect.
    case lost(retrying: Bool)
}

/// What a tab's terminal receives, in order.
public enum TabTerminalEvent: Equatable, Sendable {
    /// Start over: clear the terminal, set this size, then draw `seed`: the
    /// tab's history and screen as the box has it, with the OSC 133 marks the
    /// agent knows replayed in place. Always the first event, and again after
    /// every reconnect.
    case reset(TabTerminalSize, seed: [UInt8])
    /// The tab's output.
    case output([UInt8])
    /// The tab's size changed elsewhere (another view of the tab, or another
    /// viewer): the terminal takes it, and the view shows what fits.
    case resized(TabTerminalSize)
    /// The stream ended; nothing follows.
    case ended(TabTerminalEnd)
}

/// One tab's terminal: events from the box, input and size to it.
@MainActor
public protocol TabTerminalSource: AnyObject {
    /// The tab's ID (tmux's window ID, as in TabInfo).
    var tab: String { get }
    /// The events, from `reset` on. One consumer.
    var events: AsyncStream<TabTerminalEvent> { get }
    /// Keystrokes, pasted text and mouse reports, exactly as the terminal
    /// emulator produced them.
    func send(_ input: [UInt8])
    /// The size this view can show. The tab takes the latest size asked for.
    func resize(_ size: TabTerminalSize)
    /// Stops the stream. The tab keeps running.
    func close()
    /// Every paste goes between bracketed-paste markers, whatever the
    /// terminal's own idea of the mode: the workspace's helper hands it to
    /// tmux, which brackets it only if the program asked (CC-0490).
    var marksPastes: Bool { get }
}

public extension TabTerminalSource {
    var marksPastes: Bool { false }
}

/// Opens tab terminals for one workspace.
@MainActor
public protocol TabTerminalProvider: AnyObject {
    func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource
}

/// A source that plays given events and records what the view sends: for
/// tests, renders and split view's work before the control-mode provider
/// exists.
@MainActor
public final class ScriptedTabSource: TabTerminalSource {
    public let tab: String
    public let events: AsyncStream<TabTerminalEvent>
    private let continuation: AsyncStream<TabTerminalEvent>.Continuation
    public private(set) var sent: [[UInt8]] = []
    public private(set) var sizes: [TabTerminalSize] = []
    public private(set) var closed = false
    public var marksPastes = false

    public init(tab: String) {
        self.tab = tab
        let stream = AsyncStream.makeStream(of: TabTerminalEvent.self)
        events = stream.stream
        continuation = stream.continuation
    }

    /// Plays an event, as the box would send it.
    public func play(_ event: TabTerminalEvent) {
        continuation.yield(event)
        if case .ended = event { continuation.finish() }
    }

    public func send(_ input: [UInt8]) { sent.append(input) }
    public func resize(_ size: TabTerminalSize) { sizes.append(size) }

    public func close() {
        closed = true
        continuation.finish()
    }
}
