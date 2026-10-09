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
        cli: CLI(),
        notifier: Notifier.shared
    )

    var body: some Scene {
        WindowGroup {
            MainWindow(controller: controller)
                .task {
                    delegate.controller = controller
                    await Daemon.shared.ensureRunning()
                    // Before opening: with no network at all the box opens
                    // offline at once instead of probing storage.
                    await NetworkWatch.shared.start()
                    await controller.open()
                    // The state line follows the recorded save state.
                    while !Task.isCancelled {
                        try? await Task.sleep(for: .seconds(3))
                        if !controller.busy { await controller.refresh() }
                    }
                }
        }
        .windowToolbarStyle(.unified(showsTitle: true))
        .commands {
            // Make a Save Point (⌘S) is in the Box menu (§3.2), not File.
            CommandGroup(replacing: .saveItem) {}
            // The title menu's box actions, also in the menu bar.
            CommandMenu("Box") {
                BoxMenu(controller: controller)
            }
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var signals: [DispatchSourceSignal] = []
    /// The window's box: quitting closes it, waking checks it.
    var controller: BoxController?

    func applicationDidFinishLaunching(_: Notification) {
        // A Swift package executable starts as a background process; make
        // it a regular app with a Dock icon and a menu bar.
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
        // SIGTERM (Xcode's Stop, logout, kill) and SIGINT (Ctrl-C in the
        // Terminal that started it) quit like ⌘Q, so open boxes are saved
        // and released.
        for sig in [SIGTERM, SIGINT] {
            signal(sig, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: sig, queue: .main)
            source.setEventHandler { NSApp.terminate(nil) }
            source.resume()
            signals.append(source)
        }
        // After sleep, portenvd checks the box agent's channel at once, so a
        // channel that dropped while the Mac slept never goes unnoticed.
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let controller = self?.controller else { return }
                Task { await controller.woke() }
            }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_: NSApplication) -> Bool { true }

    func applicationShouldTerminate(_: NSApplication) -> NSApplication.TerminateReply {
        // Quitting never fails silently: close (save and release) the box
        // first; if that fails, ask (Try Again, Quit Anyway, Cancel). Then
        // stop portenvd, which closes anything left; a box it still can't
        // save keeps a marker and is saved first thing on the next launch.
        // While AppKit waits for the reply it runs the main run loop in a
        // modal mode, where main-actor tasks do not run: work off the main
        // actor and show the alert and the reply in that mode.
        let flow = controller?.quitFlow
        Task.detached {
            let quit = await flow?.run(ask: Self.ask) ?? true
            if quit { await Daemon.shared.stop() }
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                NSApp.reply(toApplicationShouldTerminate: quit)
            }
        }
        return .terminateLater
    }

    /// The alert when the box couldn't be saved before quitting.
    nonisolated static func ask(_ message: String) async -> QuitChoice {
        await withCheckedContinuation { done in
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                MainActor.assumeIsolated {
                    let alert = NSAlert()
                    alert.alertStyle = .warning
                    alert.messageText = message
                    alert.informativeText = "Try again, or quit now: the box stays as it is and is saved first thing the next time Portenv opens."
                    alert.addButton(withTitle: "Try Again")
                    alert.addButton(withTitle: "Quit Anyway")
                    alert.addButton(withTitle: "Cancel")
                    switch alert.runModal() {
                    case .alertFirstButtonReturn: done.resume(returning: .tryAgain)
                    case .alertSecondButtonReturn: done.resume(returning: .quitAnyway)
                    default: done.resume(returning: .cancel)
                    }
                }
            }
        }
    }
}
