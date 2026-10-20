// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// First run when there's no box, the box list when the box asked for
/// (PORTENV_BOX) isn't here, and the box otherwise (PLAN.md 1.5). Never a
/// window for a box that doesn't exist. Each workspace has a window of its
/// own, grouped as macOS window tabs (R-0344).
@main
struct PortenvApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel()

    init() {
        AppTerminal.announce()
        // A dev lane's build refuses an environment naming another lane, before
        // it touches a login item, keys or a folder (Instance, gmake app LANE=X).
        if let problem = Instance.current.mismatch(environment: ProcessInfo.processInfo.environment) {
            FileHandle.standardError.write(Data((problem + "\n").utf8))
            let alert = NSAlert()
            alert.messageText = "Portenv can’t start"
            alert.informativeText = problem
            alert.runModal()
            exit(1)
        }
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
        #if PORTENV_DEV_PREVIEWS
        // Lane builds only (R-0220): the command blocks in a window of their
        // own, with no portenvd; closing it quits.
        if CommandLine.arguments.contains(BlocksPreview.argument) {
            MainActor.assumeIsolated {
                let app = NSApplication.shared
                app.setActivationPolicy(.regular)
                BlocksPreview.show()
                app.run()
            }
            exit(0)
        }
        #endif
    }

    var body: some Scene {
        WindowGroup(id: WorkspaceWindows.id) {
            AppRoot(model: model)
                .task {
                    delegate.sessions = model.sessions
                    delegate.runningNames = { [model] in model.knownRunning }
                }
        }
        .windowToolbarStyle(.unified(showsTitle: true))
        .commands {
            // The menus act on the workspace whose window is main (R-0344).
            let current = model.sessions.current
            // Make a Save Point (⌘S) is in the Box menu (§3.2), not File.
            CommandGroup(replacing: .saveItem) {}
            // The title menu's box actions, also in the menu bar, once a box
            // is open.
            CommandMenu("Workspace") {
                if let controller = current?.controller {
                    BoxMenu(controller: controller)
                }
            }
            WorkspaceTabCommands(hasWindow: !model.sessions.all.isEmpty)
            TabCommands(tabs: current?.tabs, split: current?.split)
            SplitCommands(split: current?.split)
            InspectorToggle(box: current?.controller?.box)
            TeamViewCommands(box: current?.controller?.box, team: current?.team)
            FindCommands()
            FeedbackCommands()
            // Portenv › Settings… (⌘,): the toolbar window (R-0137).
            CommandGroup(replacing: .appSettings) {
                Button("Settings\u{2026}") { SettingsWindow.show() }
                    .keyboardShortcut(",", modifiers: .command)
            }
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var signals: [DispatchSourceSignal] = []
    /// Every window's session (R-0344): waking checks each workspace shown,
    /// and relaunching for an update leaves each running.
    var sessions: WorkspaceSessions?
    private var controlTab: Any?
    /// The workspaces the app knows are running (AppModel.knownRunning).
    var runningNames: () -> [String] = { [] }
    var knownRunning: [String] { runningNames() }
    /// Set when Portenv quits to relaunch for an update (Sparkle's
    /// updaterWillRelaunchApplication, 1.8): the box keeps running and the
    /// next portenvd takes it over (ADR 0014). Cmd-Q still closes and saves.
    var relaunchingForUpdate = false

    func applicationWillFinishLaunching(_: Notification) {
        // The display Portenv was launched from: first run opens there.
        FirstRunLaunch.pointer = NSEvent.mouseLocation
        // Workspace windows are macOS window tabs; every other window keeps
        // tabbing off (R-0344, R-0345).
        WindowCloseHook.turnOnWorkspaceTabs()
    }

    func applicationDidFinishLaunching(_: Notification) {
        // A Swift package executable starts as a background process; make
        // it a regular app with a Dock icon and a menu bar.
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
        // Windows restored from a display that's gone come back on screen.
        OnScreen.watch()
        // A read that may raise macOS's Keychain or Touch ID prompt brings
        // the window that asked to the front first, so the prompt comes up
        // over it, never unseen behind "Opening…" (R-0214).
        KeychainWait.front.handler = {
            DispatchQueue.main.async {
                NSApp.activate()
                (NSApp.keyWindow ?? NSApp.mainWindow ?? NSApp.windows.first { $0.isVisible })?.makeKeyAndOrderFront(nil)
            }
        }
        OnScreen.fixAll()
        // ⌃Tab / ⌃⇧Tab: the next and previous workspace tab (R-0345).
        controlTab = WorkspaceWindows.watchControlTab()
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
                for controller in self?.sessions?.controllers ?? [] {
                    Task { await controller.woke() }
                }
            }
        }
    }

    /// The last workspace window never closes by itself: closing it goes
    /// back to "Your workspaces" (AppModel.windowShouldClose). Closing the
    /// list or first run, the last window, quits.
    func applicationShouldTerminateAfterLastWindowClosed(_: NSApplication) -> Bool { true }

    /// The tab bar's ＋ (and Window › New Tab): "Your workspaces" in a new
    /// tab, when no window answers it first.
    @objc func newWindowForTab(_: Any?) {
        WorkspaceWindows.open?()
    }

    func applicationShouldTerminate(_: NSApplication) -> NSApplication.TerminateReply {
        // Quitting never fails silently: close (save and release) the box
        // first; if that fails, ask (Try Again, Quit Anyway, Cancel). Then
        // stop portenvd, which closes anything left; a box it still can't
        // save keeps a marker and is saved first thing on the next launch.
        // While AppKit waits for the reply it runs the main run loop in a
        // modal mode, where main-actor tasks do not run: work off the main
        // actor and show the alert and the reply in that mode.
        let controllers = sessions?.controllers ?? []
        let flow = controllers.first?.quitFlow
        // The workspaces the app knows are running, named if portenvd
        // doesn't answer (R-0252).
        let known = knownRunning
        let relaunching = relaunchingForUpdate
        // Every window's workspace keeps running for the next portenvd.
        if relaunching { controllers.forEach { $0.relaunchingForUpdate() } }
        Task.detached {
            if relaunching {
                // portenvd stops by itself, leaving the box running.
                _ = await flow?.relaunch()
                RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                    NSApp.reply(toApplicationShouldTerminate: true)
                }
                return
            }
            // Every workspace running on this Mac (with or without a window,
            // ADR 0023) is named, then closed, saved and released through
            // portenvd's API; portenvd itself stays with launchd, with
            // nothing open.
            let all = await QuitAll.prepare(SharedDaemon.client, known: known)
            var quit = true
            if let text = all.confirmation {
                quit = await Self.confirmQuit(text)
            }
            if quit { quit = await all.run(ask: Self.ask, tell: Self.tell) }
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                NSApp.reply(toApplicationShouldTerminate: quit)
            }
        }
        return .terminateLater
    }

    /// Why a close was refused (a Move To in progress, R-0249), with OK.
    nonisolated static func tell(_ message: String) async {
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                MainActor.assumeIsolated {
                    let alert = NSAlert()
                    alert.messageText = message
                    alert.addButton(withTitle: "OK")
                    alert.runModal()
                    done.resume()
                }
            }
        }
    }

    /// Quit's confirmation, naming the running workspaces (R-0238): Quit is
    /// the default.
    nonisolated static func confirmQuit(_ text: String) async -> Bool {
        await withCheckedContinuation { done in
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                MainActor.assumeIsolated {
                    let alert = NSAlert()
                    alert.messageText = "Quit Portenv?"
                    alert.informativeText = text
                    alert.addButton(withTitle: "Quit")
                    alert.addButton(withTitle: "Cancel")
                    done.resume(returning: alert.runModal() == .alertFirstButtonReturn)
                }
            }
        }
    }

    /// The alert when the box couldn't be saved before quitting.
    nonisolated static func ask(_ message: String) async -> QuitChoice {
        await ask(message,
                  informative: "Try again, or quit now: the workspace stays as it is and is saved first thing the next time Portenv opens.",
                  anyway: "Quit Anyway")
    }

    /// The alert when the box couldn't be saved before its window went back
    /// to the box list (WindowClose).
    nonisolated static func askOnClose(_ message: String) async -> QuitChoice {
        await ask(message, informative: WindowClose.informative, anyway: WindowClose.anywayButton)
    }

    nonisolated static func ask(_ message: String, informative: String, anyway: String) async -> QuitChoice {
        await withCheckedContinuation { done in
            RunLoop.main.perform(inModes: [.default, .modalPanel]) {
                MainActor.assumeIsolated {
                    let alert = NSAlert()
                    alert.alertStyle = .warning
                    alert.messageText = message
                    alert.informativeText = informative
                    alert.addButton(withTitle: "Try Again")
                    alert.addButton(withTitle: anyway)
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
