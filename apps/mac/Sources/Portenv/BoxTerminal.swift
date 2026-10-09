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

    func makeNSView(context: Context) -> LocalProcessTerminalView {
        let view = LocalProcessTerminalView(frame: .zero)
        view.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        context.coordinator.onEnded = onEnded
        context.coordinator.attach(view, box: box, generation: generation)
        return view
    }

    func updateNSView(_ view: LocalProcessTerminalView, context: Context) {
        if context.coordinator.generation != generation {
            context.coordinator.attach(view, box: box, generation: generation)
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
                args: ["attach", box, "--from-app"],
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
