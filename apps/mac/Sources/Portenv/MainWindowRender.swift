// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// The main window for `--render-screens` (dev builds): the real MainWindow,
/// in a real titled window with its unified toolbar, fed by a sample
/// portenvd and showing the sample terminal. Drawn once per appearance,
/// next to the main-window board in PRs. No keys, no boxes, no portenvd.
/// It's captured by the window server (`screencapture -l`), since an
/// in-process capture leaves out the system's materials (the toolbar's and
/// the inspector's glass): on the sharpest screen, so it's drawn at that
/// screen's scale (@2x on a Retina display), at desktop level behind every
/// other window (the capture reads the window's own content even where it's
/// covered), and drawn as the active window without taking focus. The
/// traffic lights still draw inactive: they follow the app's own activity,
/// and a render never brings the app forward.
@MainActor
enum MainWindowRender {
    /// The board's window: 1280 × 890 pt with the inspector open.
    static let size = NSSize(width: 1280, height: 890)

    static func render(to dir: URL, name: String = "main-window", inspector: Bool = true,
                       terminal: @escaping () -> NSView) async throws {
        let box = "acme-api"
        UserDefaults.standard.set(inspector, forKey: InspectorToggle.key(box))
        // The board's clock: saved at 14:58 today, drawn at 15:02.
        let today = Calendar.current.startOfDay(for: Date())
        let drawnAt = today.addingTimeInterval(15 * 3600 + 2 * 60)
        let savedAt = today.addingTimeInterval(14 * 3600 + 58 * 60)
        let before = NSApp.appearance
        defer { NSApp.appearance = before }
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            // The terminal takes its palette from the app's appearance.
            NSApp.appearance = NSAppearance(named: appearance)
            let daemon = SampleDaemon(savedAt: savedAt)
            let controller = BoxController(box: box, daemon: daemon, keychain: NoKeychain(), now: { drawnAt })
            let tabs = TabsController(box: box, daemon: daemon)
            await controller.open()
            let follow = Task { await tabs.follow() }
            defer { follow.cancel() }

            let window = ActiveLookingWindow(contentRect: NSRect(origin: .zero, size: size),
                                  styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                                  backing: .buffered, defer: false)
            window.isReleasedWhenClosed = false
            window.toolbarStyle = .unified
            window.appearance = NSAppearance(named: appearance)
            let host = NSHostingView(rootView: MainWindow(controller: controller, tabs: tabs, renderTerminal: terminal))
            host.sceneBridgingOptions = [.toolbars, .title]
            window.contentView = host
            window.setContentSize(size)
            // Behind everything on the sharpest screen: drawn at its scale,
            // covered by the person's windows, never key.
            window.level = NSWindow.Level(rawValue: Int(CGWindowLevelForKey(.desktopWindow)))
            // The sharpest screen (the built-in Retina display, say), not
            // the one with the key window.
            if let screen = NSScreen.screens.max(by: { $0.backingScaleFactor < $1.backingScaleFactor }) {
                window.setFrameOrigin(NSPoint(x: screen.visibleFrame.minX, y: screen.visibleFrame.minY))
            }
            window.orderBack(nil)
            defer { window.orderOut(nil) }
            // Traffic lights and the title follow the app's activity, not
            // the window's: AppKit's switch for drawing them active, if this
            // macOS has it (renders only; never in the app).
            let force = Selector(("_setForceActiveControls:"))
            if window.responds(to: force) { window.perform(force, with: true as NSNumber) }
            try? await Task.sleep(for: .seconds(1.5))

            let file = dir.appendingPathComponent("\(name)-\(suffix).png")
            let shot = Process()
            shot.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
            // -o: no window shadow; -x: no sound.
            shot.arguments = ["-x", "-o", "-l", String(window.windowNumber), file.path]
            try shot.run()
            shot.waitUntilExit()
            guard shot.terminationStatus == 0, FileManager.default.fileExists(atPath: file.path) else {
                throw FirstRunError("screencapture failed for \(name) (Screen Recording permission?)")
            }
        }
    }
}

/// A window drawn as the active one (coloured traffic lights, focus states)
/// without being made key, so a render never takes focus.
private final class ActiveLookingWindow: NSWindow {
    override var isKeyWindow: Bool { true }
    override var isMainWindow: Bool { true }
}

/// An AppKit view in SwiftUI, made once (renders only).
struct RenderedView: NSViewRepresentable {
    let make: () -> NSView
    func makeNSView(context _: Context) -> NSView { make() }
    func updateNSView(_: NSView, context _: Context) {}
}

/// Renders read no Keychain.
private struct NoKeychain: KeychainReading {
    func read(account _: String) throws -> Data? { nil }
}

/// portenvd as the main-window board shows it: acme-api open on this Mac,
/// saved a few minutes ago, with the board's three tabs.
private final class SampleDaemon: DaemonAPI, @unchecked Sendable {
    private let savedAt: Date
    init(savedAt: Date) { self.savedAt = savedAt }
    private let tabList = [
        TabInfo(id: "@0", name: "claude", active: true, program: "claude"),
        TabInfo(id: "@1", name: "shell", active: false),
        TabInfo(id: "@2", name: "muse", active: false),
    ]

    private var stateNow: BoxState {
        var s = BoxState(save: .saved, savedAt: savedAt)
        s.storagePlace = "this Mac"
        s.openedAt = savedAt.addingTimeInterval(-5 * 3600)
        return s
    }

    func ping() async -> Bool { true }
    func boxes() async throws -> [String] { ["acme-api"] }
    func servers() async throws -> [String] { ["portenv@test-server"] }
    func open(_: String) async throws {}
    func close(_: String) async throws {}
    func makeSavePoint(_: String) async throws {}
    func revert(_: String) async throws -> Date? { nil }
    func move(_: String, to _: String) async throws {}
    func check(_: String) async throws -> Bool { true }
    func restart(_: String) async throws {}
    func state(_: String) async throws -> BoxState { stateNow }
    func watch(_: String) -> AsyncThrowingStream<BoxState, Error> {
        let s = stateNow
        return AsyncThrowingStream { $0.yield(s) }
    }
    func keyIDs(_: String) async throws -> [String] { [] }
    func provideKeys(_: String, _: [String: Data]) async throws {}
    func retryPackages(_: String) async throws {}
    func saves(_: String) async throws -> [SaveInfo] {
        [
            SaveInfo(id: "a", time: savedAt, kind: "autosave", machine: "studio"),
            SaveInfo(id: "p", time: savedAt.addingTimeInterval(-27 * 60), kind: "point", machine: "studio", name: "before retry change"),
            SaveInfo(id: "o", time: savedAt.addingTimeInterval(-66 * 60), kind: "orphaned", machine: "studio"),
        ]
    }
    func setNetwork(usable _: Bool) async throws {}
    func woke() async throws -> Bool { false }
    func relaunch() async throws {}
    func leaveUnsaved(_: String) async throws {}
    func takeSavedAfterQuit() async throws -> [SavedAfterQuit] { [] }
    func tabs(_: String) async throws -> [TabInfo] { tabList }
    func newTab(_: String, name: String) async throws -> TabInfo { TabInfo(id: "@9", name: name, active: true) }
    func closeTab(_: String, id _: String) async throws {}
    func renameTab(_: String, id _: String, name _: String) async throws {}
    func selectTab(_: String, id _: String) async throws {}
    func watchTabs(_: String) -> AsyncThrowingStream<[TabInfo], Error> {
        let t = tabList
        return AsyncThrowingStream { $0.yield(t) }
    }
}
