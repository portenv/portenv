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
            .navigationSubtitle(controller.subtitle)
            // The title is a menu only in a window with a toolbar; its one
            // item is the sync symbol next to the title (docs/PLAN.md, Main
            // window).
            .toolbar {
                ToolbarItem(placement: .navigation) {
                    Image(systemName: controller.busy ? "arrow.triangle.2.circlepath" : "checkmark.circle")
                        .foregroundStyle(.secondary)
                        .help(controller.subtitle)
                }
            }
            .toolbarTitleMenu {
                Menu("Move To") {
                    Button("This Mac") { Task { await controller.move(to: "this-mac") } }
                        .disabled(controller.location == .thisMac)
                    if !controller.servers.isEmpty { Divider() }
                    ForEach(controller.servers, id: \.self) { server in
                        Button(server) { Task { await controller.move(to: server) } }
                            .disabled(controller.location == .server(server))
                    }
                }
                Menu("Revert To") {
                    Button("Last Save Point") { Task { await controller.revertToLastSavePoint() } }
                        .disabled(controller.location != .thisMac)
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
        case .thisMac:
            BoxTerminal(box: controller.box, generation: controller.terminalGeneration)
        case .server(let server):
            placeholder("\(controller.box) is open on \(server)", "Move To ▸ This Mac brings it back.")
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
