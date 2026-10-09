// SPDX-License-Identifier: Apache-2.0

import PortenvKit
import SwiftUI

/// The main window: a full-width terminal under a title with the box's name
/// and a subtitle with its state. Box actions live in the title menu
/// (Apple's document-menu pattern); in 1.0 only Move To and Revert To work.
struct MainWindow: View {
    @Bindable var controller: BoxController

    var body: some View {
        content
            .frame(minWidth: 640, minHeight: 400)
            .navigationTitle(controller.box)
            // The title is the box's menu (Apple's document-menu pattern):
            // the system title is replaced by BoxTitle, the box's name over
            // its state line, next to the sync symbol.
            .toolbar(removing: .title)
            .toolbar {
                ToolbarItem(placement: .navigation) {
                    Image(systemName: controller.busy ? "arrow.triangle.2.circlepath" : "checkmark.circle")
                        .foregroundStyle(.secondary)
                        .help(controller.subtitle)
                }
                ToolbarItem(placement: .navigation) {
                    BoxTitle(controller: controller)
                }
            }
            .disabled(controller.busy)
            .onDisappear { Task { await controller.close() } }
            .alert("Portenv", isPresented: Binding(get: { controller.error != nil }, set: { if !$0 { controller.dismissError() } })) {
                Button("OK") { controller.dismissError() }
            } message: {
                Text(controller.error ?? "")
            }
    }

    @ViewBuilder private var content: some View {
        switch controller.location {
        case _ where controller.agentUnavailable && controller.isOpen:
            VStack(spacing: 10) {
                Text("The box agent is unavailable").font(.title3)
                Text("Nothing can be saved until the box restarts. Your unsaved changes stay in the box; restarting keeps them.")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                Button("Restart Box") { Task { await controller.restartBox() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(controller.busy)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(.background)
        case .thisMac, .server:
            // The same terminal wherever the box runs: portenvd reaches a
            // server box's agent through an SSH forward (ADR 0010).
            BoxTerminal(box: controller.box, generation: controller.terminalGeneration) {
                Task { await controller.terminalEnded() }
            }
        case .closed:
            placeholder(controller.busy ? controller.subtitle : "\(controller.box) is closed", "")
        }
    }

    private func placeholder(_ title: String, _ detail: String) -> some View {
        VStack(spacing: 6) {
            Text(title).font(.title3)
            if !detail.isEmpty { Text(detail).foregroundStyle(.secondary) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background)
    }
}

/// The box's actions, shared by the title menu and the Box menu in the
/// menu bar. In 1.0 only Move To and Revert To work.
struct BoxMenu: View {
    @Bindable var controller: BoxController

    var body: some View {
        Menu("Move To") {
            // The current location is checked (macOS convention); every
            // other location stays available.
            target("This Mac", "this-mac")
            if !controller.servers.isEmpty { Divider() }
            ForEach(controller.servers, id: \.self) { server in
                target(server, server)
            }
        }
        Menu("Revert To") {
            Button("Last Save Point") { Task { await controller.revertToLastSavePoint() } }
                .disabled(!controller.isOpen || controller.busy)
        }
        Divider()
        Button("Restart Box") { Task { await controller.restartBox() } }
            .disabled(!controller.isOpen || controller.busy)
    }

    private func target(_ title: String, _ id: String) -> some View {
        Toggle(title, isOn: Binding(
            get: { controller.isCurrent(id) },
            set: { on in if on { Task { await controller.move(to: id) } } }
        ))
        .disabled(controller.busy)
    }
}
