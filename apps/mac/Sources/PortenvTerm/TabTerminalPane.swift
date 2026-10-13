// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The NSView a pane holds (ADR 0020): one tab's terminal view that keeps its
/// place while its source changes, and reports focus.
///
/// - `show(_:)` swaps the source in place (detach, then attach); the view,
///   its scroll position and selection belong to the pane, not the tab.
/// - Focus follows the window's first responder: a click in the terminal
///   focuses the pane, and `focus()` makes the terminal first responder.
@MainActor
public final class TabTerminalHost: NSView {
    public let terminal = TerminalViews.tab()
    /// Called when the terminal gains or loses the keyboard focus.
    public var onFocusChange: (Bool) -> Void = { _ in }
    private var watch: NSKeyValueObservation?
    private var focused = false

    public override init(frame: NSRect) {
        super.init(frame: frame)
        terminal.frame = bounds
        terminal.autoresizingMask = [.width, .height]
        addSubview(terminal)
    }

    @available(*, unavailable)
    required init?(coder _: NSCoder) { fatalError("not used") }

    /// Shows another tab (or the same tab again) in this pane.
    public func show(_ source: any TabTerminalSource) {
        if terminal.source === source { return }
        terminal.attach(source)
    }

    public func focus() {
        guard let window, window.firstResponder !== terminal else { return }
        window.makeFirstResponder(terminal)
    }

    public override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        watch = window?.observe(\.firstResponder, options: [.initial, .new]) { [weak self] window, _ in
            MainActor.assumeIsolated { self?.firstResponderChanged(window.firstResponder) }
        }
    }

    func firstResponderChanged(_ responder: NSResponder?) {
        let now = responder === terminal
        if now != focused {
            focused = now
            onFocusChange(now)
        }
    }
}

/// A split-view pane's terminal in SwiftUI (ADR 0020).
///
/// - `source`: a new one swaps in place, without recreating the view.
/// - `label`: what VoiceOver reads ("Pane: grok tab, Grok Bot is typing").
/// - `isFocused`: two-way; true makes the terminal first responder, and a
///   click in the terminal sets it.
/// - `onReset`/`onEnded`: for the pane header ("Reconnecting…",
///   "Disconnected" with Reconnect).
public struct TabTerminalPane: NSViewRepresentable {
    public let source: any TabTerminalSource
    public let label: String
    @Binding public var isFocused: Bool
    public var onReset: (TabTerminalSize) -> Void
    public var onEnded: (TabTerminalEnd) -> Void

    public init(source: any TabTerminalSource, label: String, isFocused: Binding<Bool>,
                onReset: @escaping (TabTerminalSize) -> Void = { _ in },
                onEnded: @escaping (TabTerminalEnd) -> Void = { _ in }) {
        self.source = source
        self.label = label
        _isFocused = isFocused
        self.onReset = onReset
        self.onEnded = onEnded
    }

    public func makeNSView(context _: Context) -> TabTerminalHost {
        let host = TabTerminalHost(frame: .zero)
        update(host)
        return host
    }

    public func updateNSView(_ host: TabTerminalHost, context _: Context) {
        update(host)
    }

    private func update(_ host: TabTerminalHost) {
        host.terminal.onReset = onReset
        host.terminal.onEnded = onEnded
        host.terminal.setAccessibilityLabel(label)
        let binding = $isFocused
        host.onFocusChange = { focused in
            if binding.wrappedValue != focused { binding.wrappedValue = focused }
        }
        host.show(source)
        if isFocused { host.focus() }
    }
}
