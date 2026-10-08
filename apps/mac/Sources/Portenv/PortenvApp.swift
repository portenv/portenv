// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// Milestone 1.0: one window for one box (PORTENV_BOX, default "demo"; make
/// it first with `portenv init`). No first run yet (1.5).
@main
struct PortenvApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var controller = BoxController(
        box: ProcessInfo.processInfo.environment["PORTENV_BOX"] ?? "demo",
        cli: CLI()
    )

    var body: some Scene {
        WindowGroup {
            MainWindow(controller: controller)
                .task {
                    await Daemon.shared.ensureRunning()
                    await controller.open()
                }
        }
        .windowToolbarStyle(.unified(showsTitle: true))
        .commands {
            CommandGroup(replacing: .saveItem) {
                Button("Make Save Point") { Task { await controller.makeSavePoint() } }
                    .keyboardShortcut("s")
                    .disabled(controller.location != .thisMac || controller.busy)
            }
            // The title menu's box actions, also in the menu bar.
            CommandMenu("Box") {
                BoxMenu(controller: controller)
            }
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var sigterm: DispatchSourceSignal?

    func applicationDidFinishLaunching(_: Notification) {
        // A Swift package executable starts as a background process; make
        // it a regular app with a Dock icon and a menu bar.
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
        // SIGTERM (Xcode's Stop, logout, kill) quits like ⌘Q, so open boxes
        // are saved and released.
        signal(SIGTERM, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
        source.setEventHandler { NSApp.terminate(nil) }
        source.resume()
        sigterm = source
    }

    func applicationShouldTerminateAfterLastWindowClosed(_: NSApplication) -> Bool { true }

    func applicationShouldTerminate(_: NSApplication) -> NSApplication.TerminateReply {
        // portenvd closes (saves and releases) every box it opened when it
        // stops; wait for that before quitting.
        // While AppKit waits for the reply it runs the main run loop in a
        // modal mode, where main-actor tasks do not run: stop the daemon off
        // the main actor and post the reply into that mode.
        Task.detached {
            await Daemon.shared.stop()
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                NSApp.reply(toApplicationShouldTerminate: true)
            }
        }
        return .terminateLater
    }
}
