// SPDX-License-Identifier: Apache-2.0

import Observation

/// The tab a terminal view shows, and how its stream is doing (ADR 0020):
/// the main window's, which follows the tab bar's current tab.
///
/// - `show(tab:generation:)` opens the tab's stream, closing the one before;
///   the same tab and generation again does nothing.
/// - A lost stream is `.reconnecting` until its next reset, and
///   `.disconnected` (with why, and Reconnect) when it gives up.
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
        switch connection {
        case .reconnecting: true
        case .disconnected: serviceComing
        case .connecting, .live: false
        }
    }

    public enum Connection: Equatable, Sendable {
        case connecting, live, reconnecting
        case disconnected(String?)
    }

    public private(set) var source: (any TabTerminalSource)?
    public private(set) var connection = Connection.connecting
    /// The size the last reset gave: the next stream opens at it.
    public private(set) var size = TabTerminalSize(cols: 80, rows: 24)
    @ObservationIgnored private let provider: any TabTerminalProvider
    @ObservationIgnored private var shown: (tab: String, generation: Int)?

    public init(provider: any TabTerminalProvider) {
        self.provider = provider
    }

    public func show(tab: String, generation: Int) {
        if let shown, shown.tab == tab, shown.generation == generation, source != nil { return }
        shown = (tab, generation)
        open()
    }

    /// Reconnect: the shown tab's stream again.
    public func reconnect() { open() }

    public func stop() {
        source?.close()
        source = nil
        shown = nil
    }

    public func reset(_ size: TabTerminalSize) {
        self.size = size
        connection = .live
    }

    public func ended(_ why: TabTerminalEnd) {
        switch why {
        case .lost(retrying: true):
            connection = .reconnecting
        case .lost(retrying: false):
            connection = .disconnected((source as? ControlModeTabSource)?.failure)
        case .tabClosed, .boxStopped:
            // The tab bar moves to another tab, or the window shows the
            // stopped workspace: either replaces this stream.
            break
        }
    }

    private func open() {
        guard let shown else { return }
        source?.close()
        connection = .connecting
        source = provider.open(tab: shown.tab, size: size)
    }
}
