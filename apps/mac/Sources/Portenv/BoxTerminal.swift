// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftTerm
import SwiftUI

/// The box's tmux session in a SwiftTerm terminal. The terminal runs
/// `portenv attach`, which reaches the session through portenvd and the box
/// agent's channel (never docker exec). A new generation (the box started
/// again) reattaches.
struct BoxTerminal: NSViewRepresentable {
    let box: String
    let generation: Int
    /// Called when `portenv attach` ends (the session closed, or the box
    /// agent went away).
    var onEnded: @MainActor () -> Void = {}
    /// The link under the pointer (or one just refused), for the state line.
    var onHoverLink: @MainActor (String?) -> Void = { _ in }

    func makeNSView(context: Context) -> PaddedTerminal {
        let view = PaddedTerminal(margin: TerminalLayout.margin)
        view.onHoverLink = onHoverLink
        view.terminal.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        context.coordinator.onEnded = onEnded
        context.coordinator.attach(view.terminal, box: box, generation: generation)
        return view
    }

    func updateNSView(_ view: PaddedTerminal, context: Context) {
        if context.coordinator.generation != generation {
            context.coordinator.attach(view.terminal, box: box, generation: generation)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    @MainActor
    final class Coordinator: NSObject, LocalProcessTerminalViewDelegate {
        var generation = -1
        var onEnded: @MainActor () -> Void = {}

        func attach(_ view: LocalProcessTerminalView, box: String, generation: Int) {
            self.generation = generation
            view.processDelegate = self
            if view.process?.running == true { view.process?.terminate() }
            var env = ProcessInfo.processInfo.environment
            env["TERM"] = "xterm-256color"
            env.merge(Instance.current.environment) { _, new in new }
            view.startProcess(
                executable: Binaries.portenv.path,
                args: ["attach", box, "--from-app", "--viewer", TabViewer.app],
                environment: env.map { "\($0.key)=\($0.value)" },
                execName: "portenv"
            )
        }

        nonisolated func sizeChanged(source _: LocalProcessTerminalView, newCols _: Int, newRows _: Int) {}
        nonisolated func setTerminalTitle(source _: LocalProcessTerminalView, title _: String) {}
        nonisolated func hostCurrentDirectoryUpdate(source _: SwiftTerm.TerminalView, directory _: String?) {}
        nonisolated func processTerminated(source _: SwiftTerm.TerminalView, exitCode _: Int32?) {
            Task { @MainActor in self.onEnded() }
        }
    }
}

/// The terminal inset by a margin (GUIDELINES.md §4.5) painted in the
/// terminal's own background, so the text never touches the window's edge
/// and the margin reads as part of the terminal. A click in the margin
/// focuses the terminal.
///
/// Its colours match the system (the owner, 2026-10-11), like Terminal.app's
/// Basic profile: the system's text background and text colours, the light
/// or dark ANSI palette, and the accent for the caret and selection. They
/// change with the appearance (View › Terminal Appearance comes later).
final class PaddedTerminal: NSView {
    let terminal: LocalProcessTerminalView
    /// No program in the workspace reads or sets the Mac's clipboard, or
    /// opens a link by itself (TerminalGuard).
    let terminalGuard: TerminalGuard
    /// The link under the pointer, for the state line, or nil.
    var onHoverLink: (String?) -> Void = { _ in }
    private var hovered: String?

    init(margin: CGFloat) {
        (terminal, terminalGuard) = TerminalViews.attached()
        super.init(frame: .zero)
        terminalGuard.onRefusedLink = { [weak self] link in self?.setHover(link) }
        wantsLayer = true
        matchSystemAppearance()
        terminal.translatesAutoresizingMaskIntoConstraints = false
        addSubview(terminal)
        NSLayoutConstraint.activate([
            terminal.leadingAnchor.constraint(equalTo: leadingAnchor, constant: margin),
            terminal.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -margin),
            terminal.topAnchor.constraint(equalTo: topAnchor, constant: margin),
            terminal.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -margin),
        ])
    }

    @available(*, unavailable)
    required init?(coder _: NSCoder) { fatalError("not used") }

    override var wantsUpdateLayer: Bool { true }

    override func updateLayer() {
        layer?.backgroundColor = terminal.nativeBackgroundColor.cgColor
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        matchSystemAppearance()
    }

    /// Sets the terminal's colours for the current appearance, resolved (a
    /// dynamic colour would follow whatever appearance draws next), then
    /// redraws the text and the margin.
    private func matchSystemAppearance() {
        let system = TerminalPalette.systemColours(effectiveAppearance)
        terminal.nativeBackgroundColor = NSColor(system.background)
        terminal.nativeForegroundColor = NSColor(system.foreground)
        terminal.caretColor = NSColor(system.caret)
        terminal.selectedTextBackgroundColor = NSColor(system.selection)
        // installColors also drops the terminal's cached colours and redraws.
        terminal.installColors(TerminalPalette.matching(effectiveAppearance).ansi.map { SwiftTerm.Color($0) })
        terminal.needsDisplay = true
        needsDisplay = true
    }

    override func mouseDown(with _: NSEvent) {
        window?.makeFirstResponder(terminal)
    }

    // MARK: Links (R-0134): the full destination before anyone clicks.

    private var tracking: NSTrackingArea?

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if tracking == nil {
            let area = NSTrackingArea(rect: .zero, options: [.mouseMoved, .mouseEnteredAndExited, .activeInKeyWindow, .inVisibleRect],
                                      owner: self, userInfo: nil)
            addTrackingArea(area)
            tracking = area
        }
    }

    override func mouseMoved(with event: NSEvent) {
        super.mouseMoved(with: event)
        setHover(terminalGuard.link(atWindowPoint: event.locationInWindow))
    }

    override func mouseExited(with event: NSEvent) {
        super.mouseExited(with: event)
        setHover(nil)
    }

    /// The tooltip and the state line show the link's full destination, or
    /// why Portenv won't open it.
    private func setHover(_ link: String?) {
        guard link != hovered else { return }
        hovered = link
        terminal.toolTip = link.map(TerminalLinks.status)
        onHoverLink(link)
    }
}

extension NSColor {
    convenience init(_ c: RGB) {
        self.init(srgbRed: CGFloat(c.red) / 255, green: CGFloat(c.green) / 255, blue: CGFloat(c.blue) / 255, alpha: 1)
    }
}

extension SwiftTerm.Color {
    convenience init(_ c: RGB) {
        self.init(red8: UInt16(c.red), green8: UInt16(c.green), blue8: UInt16(c.blue))
    }
}
