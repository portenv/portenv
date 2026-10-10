// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// One window: first run when there's no box, the box list when the box
/// asked for (PORTENV_BOX) isn't here, and the box otherwise (PLAN.md 1.5).
/// Never a window for a box that doesn't exist.
@main
struct PortenvApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel()

    init() {
        // Dev builds and tests: unregister the login item and exit, with no
        // window (make unregister-service, portenv app unregister-service).
        if CommandLine.arguments.contains("--unregister-service") {
            exit(LoginItem.unregister())
        }
        // Dev builds: write light and dark renders of the first-run screens
        // and the box list to a folder, then exit (for PRs, next to the
        // mockups).
        if let i = CommandLine.arguments.firstIndex(of: "--render-screens"), i + 1 < CommandLine.arguments.count {
            exit(MainActor.assumeIsolated { ScreenRenders.write(to: CommandLine.arguments[i + 1]) })
        }
    }

    var body: some Scene {
        WindowGroup {
            AppRoot(model: model)
                .task {
                    model.onOpen = { [delegate] controller in delegate.controller = controller }
                    await model.start()
                }
        }
        .windowToolbarStyle(.unified(showsTitle: true))
        .commands {
            // Make a Save Point (⌘S) is in the Box menu (§3.2), not File.
            CommandGroup(replacing: .saveItem) {}
            // The title menu's box actions, also in the menu bar, once a box
            // is open.
            CommandMenu("Box") {
                if let controller = model.controller {
                    BoxMenu(controller: controller)
                }
            }
            TabCommands(tabs: model.tabs)
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var signals: [DispatchSourceSignal] = []
    /// The window's box: quitting closes it, waking checks it.
    var controller: BoxController?
    /// Set when Portenv quits to relaunch for an update (Sparkle's
    /// updaterWillRelaunchApplication, 1.8): the box keeps running and the
    /// next portenvd takes it over (ADR 0014). Cmd-Q still closes and saves.
    var relaunchingForUpdate = false

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
        let relaunching = relaunchingForUpdate
        if relaunching { controller?.relaunchingForUpdate() }
        Task.detached {
            if relaunching {
                // portenvd stops by itself, leaving the box running.
                _ = await flow?.relaunch()
                RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                    NSApp.reply(toApplicationShouldTerminate: true)
                }
                return
            }
            // The box is closed, saved and released through portenvd's API;
            // portenvd itself stays with launchd, with nothing open.
            let quit = await flow?.run(ask: Self.ask) ?? true
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
