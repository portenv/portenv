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

    func makeNSView(context: Context) -> PaddedTerminal {
        let view = PaddedTerminal(margin: TerminalLayout.margin)
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
final class PaddedTerminal: NSView {
    let terminal = LocalProcessTerminalView(frame: .zero)

    init(margin: CGFloat) {
        super.init(frame: .zero)
        wantsLayer = true
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

    override func mouseDown(with _: NSEvent) {
        window?.makeFirstResponder(terminal)
    }
}
