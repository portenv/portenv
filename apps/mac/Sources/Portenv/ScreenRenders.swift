// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// `Portenv --render-screens DIR` (dev builds): light and dark PNGs of each
/// first-run step, the box list, the inspector and the terminal's colours,
/// laid out in an offscreen window, for PRs next to the mockups. Uses
/// samples: no keys, no boxes, no portenvd.
@MainActor
enum ScreenRenders {
    /// ADR 0019's vector key, as on the board (R-0129), only ever drawn into renders; built from
    /// groups so no key-shaped literal is in the source.
    /// Never finishes making the key: Welcome's spinner, for its render.
    private struct Slow: FirstRunService {
        func makeRecoveryKey() async throws -> String {
            try await Task.sleep(for: .seconds(3600))
            return ""
        }
        func createBox(_: NewBox) async throws {}
    }

    /// Renders call nothing.
    private struct NoChecks: BoxCreating, RecoveryChecking {
        func listings() async throws -> [BoxListing] { [] }
        func createBox(_: CreateRequest) async throws -> Bool { false }
        func discardCreation(boxID _: String) async throws {}
        func checkRecoveryKey(_: [String: Data]) async throws -> RecoveryCheckResult { RecoveryCheckResult(checked: 0, opened: false) }
    }

    private struct NoSecrets: SecretStore {
        func read(account _: String) throws -> Data? { nil }
        func add(account _: String, data _: Data, label _: String) throws {}
        func set(account _: String, data _: Data, label _: String) throws {}
        func delete(account _: String) throws {}
    }

    private struct Sample: FirstRunService {
        func makeRecoveryKey() async throws -> String { ["K7QF", "M2XA", "9TRD", "HV4P", "W8LC", "3NZ5"].joined(separator: "-") }
        func createBox(_: NewBox) async throws {}
    }

    static func write(to dir: String) -> Int32 {
        _ = NSApplication.shared
        var code: Int32 = 1
        var done = false
        Task { @MainActor in
            code = await render(to: URL(fileURLWithPath: dir))
            done = true
        }
        while !done { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        return code
    }

    private static func render(to dir: URL) async -> Int32 {
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            for step in FirstRunStep.allCases {
                let flow = await sampleFlow(at: step)
                try snapshot(FirstRunView(flow: flow, opened: { _ in }, followsDocker: false),
                             name: "first-run-\(step.rawValue + 1)-\(name(step))", in: dir)
            }
            // Welcome while Docker Desktop isn't running, or isn't
            // installed (R-0098's boards).
            for (status, suffix) in [(DockerStatus.notRunning, "not-running"), (.missing, "missing"), (.starting, "starting")] {
                let flow = FirstRunFlow(service: Sample(), docker: status)
                try snapshot(FirstRunView(flow: flow, opened: { _ in }, followsDocker: false),
                             name: "first-run-1-welcome-docker-\(suffix)", in: dir)
            }
            // The first workspace waits for Docker Desktop to be ready (R-0211).
            let firstBoxStarting = FirstRunFlow(service: Sample(), at: .firstBox, docker: .starting)
            firstBoxStarting.boxName = "acme-api"
            try snapshot(FirstRunView(flow: firstBoxStarting, opened: { _ in }, followsDocker: false),
                         name: "first-run-4-first-box-docker-starting", in: dir)
            // Welcome when portenvd doesn't answer the check (R-0110).
            let gone = FirstRunFlow(service: Sample(), docker: nil)
            await gone.checkDocker { nil }
            await gone.checkDocker { nil } // two misses in a row
            try snapshot(FirstRunView(flow: gone, opened: { _ in }, followsDocker: false),
                         name: "first-run-1-welcome-service-down", in: dir)
            // A key shown in an earlier launch that quit mid-flow (R-0132).
            let memory = NoFirstRunMemory()
            memory.keyShown = true
            let replaced = FirstRunFlow(service: Sample(), memory: memory)
            await replaced.next()
            replaced.recoverySaved = true
            try snapshot(FirstRunView(flow: replaced, opened: { _ in }), name: "first-run-2-recovery-key-replaced", in: dir)
            // Welcome while the key is made (R-0132).
            let making = FirstRunFlow(service: Slow())
            Task { await making.next() }
            while !making.busy { await Task.yield() }
            try snapshot(FirstRunView(flow: making, opened: { _ in }), name: "first-run-1-welcome-making-key", in: dir)
            // A Keychain call waiting for a prompt (GUIDELINES §3.1).
            let waiting = await sampleFlow(at: .firstBox)
            waiting.keychainWaiting = true
            try snapshot(FirstRunView(flow: waiting, opened: { _ in }), name: "first-run-4-first-box-keychain-waiting", in: dir)
            // A Continue that can't move on says why, by the button (R-0126).
            let notTicked = await sampleFlow(at: .recoveryKey)
            notTicked.recoverySaved = false
            await notTicked.next()
            try snapshot(FirstRunView(flow: notTicked, opened: { _ in }), name: "first-run-2-recovery-key-not-ticked", in: dir)
            // The recovery key sheet, when the pool runs out (ADR 0019).
            let refill = RecoveryRefill(daemon: NoChecks(), pool: RecoveryPoolStore(store: NoSecrets()))
            let vector = ["K7QF", "M2XA", "9TRD", "HV4P", "W8LC", "3NZ5"].joined(separator: "-")
            try snapshot(RecoveryKeySheet(rendering: refill).frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "recovery-key-sheet",
                         size: NSSize(width: 520, height: 230), in: dir)
            try snapshot(RecoveryKeySheet(rendering: refill, typed: "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ6", line: RecoveryRefill.typo)
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "recovery-key-sheet-typo", size: NSSize(width: 520, height: 270), in: dir)
            // A valid key that isn't the one these workspaces were made with
            // (R-0167: both states a tester can reach).
            try snapshot(RecoveryKeySheet(rendering: refill, typed: "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5", line: RecoveryRefill.Outcome.wrongKey.message)
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "recovery-key-sheet-wrong-key",
                         size: NSSize(width: 520, height: 290), in: dir)
            try snapshot(RecoveryKeySheet(rendering: refill, typed: vector, line: RecoveryRefill.Outcome.nothingToCheck.message, offerNewKey: true)
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "recovery-key-sheet-nothing-to-check", size: NSSize(width: 520, height: 300), in: dir)
            try snapshot(RecoveryKeySheet(rendering: refill, newKey: vector).frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "recovery-key-sheet-new-key",
                         size: NSSize(width: 520, height: 460), in: dir)
            // 2.3, the move-and-lease board's sheets, as a tester sees them.
            let drawn = Date()
            let server = Placement.Holder.knownServer(id: "srv-0123456789ab", name: "test-server")
            let clock: (Date) -> String = { _ in "14:58" }
            let sheets: [(String, LeaseSheet, CGFloat)] = [
                ("lease-open-there", LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: drawn.addingTimeInterval(-180), answers: .yes),
                                                now: drawn, clock: clock), 330),
                ("lease-checking", LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: drawn.addingTimeInterval(-180), answers: nil),
                                              now: drawn, clock: clock), 170),
                ("lease-not-answering", LeaseSheet(box: "acme-api", placement: Placement(holder: server, newestSaveAt: drawn.addingTimeInterval(-26 * 60), answers: .no),
                                                   now: drawn, clock: clock), 330),
                ("lease-other-mac", LeaseSheet(box: "acme-api", placement: Placement(holder: .otherMachine(id: "studio-1a2b3c"), newestSaveAt: drawn.addingTimeInterval(-26 * 60), answers: .cannotAsk),
                                               now: drawn, clock: clock), 360),
            ]
            for (name, sheet, height) in sheets {
                try snapshot(LeaseSheetView(sheet: sheet, choose: { _ in }, cancel: {}).frame(maxWidth: .infinity, maxHeight: .infinity).background(.background),
                             name: name, size: NSSize(width: 440, height: height), in: dir)
            }
            let tabs = [TabInfo(id: "@0", name: "claude", active: true, program: "claude", command: "claude --continue", runningSince: drawn.addingTimeInterval(-180))]
            try snapshot(MoveConfirmationView(confirmation: MoveConfirmation(box: "acme-api", target: "ubuntu@test-server", tabs: tabs, now: drawn), move: {}, cancel: {})
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "move-confirmation", size: NSSize(width: 440, height: 290), in: dir)
            try snapshot(MoveConfirmationView(confirmation: MoveConfirmation(box: "acme-api", target: "ubuntu@test-server", tabs: [], now: drawn), move: {}, cancel: {})
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(.background), name: "move-confirmation-nothing-running", size: NSSize(width: 440, height: 250), in: dir)
            // 2.9: the launch alerts for servers' host keys (R-0180).
            let waitingKey = ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed,
                                           fingerprint: "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s", keyType: "ssh-ed25519")
            try snapshot(alert: { ServerKeyAlert.question(ServerKeyQuestion(waitingKey)) }, name: "server-key-confirm", in: dir)
            let forgotten = ServerKeyInfo(id: "srv-0123456789ab", name: "test-server", state: .unconfirmed,
                                          fingerprint: "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s", keyType: "ssh-ed25519", wasChanged: true)
            try snapshot(alert: { ServerKeyAlert.question(ServerKeyQuestion(forgotten)) }, name: "server-key-confirm-after-change", in: dir)
            try snapshot(alert: { ServerKeyAlert.notice(.mismatch("test-server")) }, name: "server-key-mismatch", in: dir)
            try snapshot(alert: { ServerKeyAlert.notice(.changed("test-server")) }, name: "server-key-changed", in: dir)
            // After an in-place update, while portenvd restarts (R-0198).
            try snapshot(ServiceUpdatingView().background(.background), name: "service-updating", size: NSSize(width: 720, height: 520), in: dir)
            // New Box… from the box list (the new-box boards).
            let newBox = FirstRunFlow(service: Sample(), at: .firstBox, existing: ["acme-api"], fromBoxList: true)
            newBox.boxName = "site"
            try snapshot(FirstRunView(flow: newBox, opened: { _ in }, cancel: {}, followsDocker: false), name: "new-box", in: dir)
            let invalid = FirstRunFlow(service: Sample(), at: .firstBox, existing: ["acme-api"], fromBoxList: true)
            invalid.boxName = "-api"
            try snapshot(FirstRunView(flow: invalid, opened: { _ in }, cancel: {}, followsDocker: false), name: "new-box-invalid", in: dir)
            try snapshot(
                BoxListView(boxes: ["acme-api", "site", "notes"], notice: LaunchDecision.missing("demo"), open: { _ in }, newBox: {}),
                name: "box-list", in: dir
            )
            // Closing the last box window goes back here, with no notice
            // (the owner, 2026-10-11).
            try snapshot(
                BoxListView(boxes: ["acme-api", "site", "notes"], notice: nil, open: { _ in }, newBox: {}),
                name: "box-list-after-closing", in: dir
            )
            // A later launch while portenvd isn't answering (R-0121): the
            // list, empty until it answers, with the notice and Try Again.
            try snapshot(
                BoxListView(boxes: [], notice: FirstRunText.serviceNotAnswering, retry: {}, open: { _ in }, newBox: {}),
                name: "launch-service-down", in: dir
            )
            // The move question, as the sheet on the window (R-0121).
            try snapshot(MoveOfferView { _ in }.background(.background), name: "move-offer",
                         size: NSSize(width: 440, height: 150), in: dir)
            try await renderSplit(to: dir)
            try renderInspector(to: dir)
            // Help › Acknowledgements…, with this build's notices (R-0119).
            try snapshot(view: { AcknowledgementsText.view(Acknowledgements.text(in: .main)) },
                         name: "acknowledgements", size: NSSize(width: 720, height: 520), in: dir)
            // The terminal paints its background on its layer, which a
            // cached display leaves out: drawn under it here.
            try snapshot(view: { sampleTerminal() }, name: "terminal", size: NSSize(width: 900, height: 452), in: dir,
                         background: { ($0 as? PaddedTerminal)?.terminal.nativeBackgroundColor })
            // The whole window, toolbar included, beside the main-window board
            // (R-0131): with the inspector open, and closed.
            try await MainWindowRender.render(to: dir, terminal: { sampleTerminal() })
            try await MainWindowRender.render(to: dir, name: "main-window-no-inspector", inspector: false, terminal: { sampleTerminal() })
            // With a team, Builder selected: the sidebar, Builder's tabs and
            // its column in the inspector's place (GUIDELINES 6.2).
            try await MainWindowRender.render(to: dir, name: "main-window-team", team: true, terminal: { sampleTerminal() })
            // A closed workspace's window: Open, and a failed open with Try
            // Again (R-0211).
            try snapshot(ClosedWorkspace(box: "acme-api", failure: nil) {}, name: "closed", size: NSSize(width: 720, height: 360), in: dir)
            // portenvd's engine line as it sends it (core/daemon/testdata/
            // engine-lines.txt), shown as is (R-0218).
            try snapshot(ClosedWorkspace(box: "acme-api", failure: "Portenv uses Docker Desktop to run workspaces on this Mac. Start Docker Desktop, then click Try Again.") {},
                         name: "closed-open-failed", size: NSSize(width: 720, height: 360), in: dir)
            // Command blocks, folded by default and opened (R-0139, R-0174).
            try BlockRenders.render(to: dir) { view, name, size in
                for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] where name.hasSuffix(suffix) {
                    try snapshotOne(view, name: String(name.dropLast(suffix.count + 1)), suffix: suffix, size: size, appearance: appearance, in: dir)
                }
            }
            // The Team sidebar (GUIDELINES 6.2): All tabs, an agent
            // selected, and compact.
            try TeamRenders.render(to: dir) { view, name, size in
                for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] where name.hasSuffix(suffix) {
                    try snapshotOne(view, name: String(name.dropLast(suffix.count + 1)), suffix: suffix, size: size, appearance: appearance, in: dir)
                }
            }
            // Portenv › Settings…, each pane (R-0137).
            try await SettingsRender.render(to: dir)
            return 0
        } catch {
            FileHandle.standardError.write(Data("render-screens: \(error)\n".utf8))
            return 1
        }
    }

    private static func sampleFlow(at step: FirstRunStep) async -> FirstRunFlow {
        switch step {
        case .welcome:
            return FirstRunFlow(service: Sample(), at: step)
        case .recoveryKey:
            let flow = FirstRunFlow(service: Sample(), at: .welcome)
            await flow.next()
            flow.recoverySaved = true
            return flow
        case .storage:
            let flow = FirstRunFlow(service: Sample(), at: .storage)
            flow.storage = .thisMac
            return flow
        case .firstBox:
            let flow = FirstRunFlow(service: Sample(), at: .storage)
            flow.storage = .thisMac
            await flow.next()
            flow.boxName = "acme-api"
            return flow
        case .gettingReady:
            let flow = FirstRunFlow(service: Sample(), at: .storage)
            flow.storage = .thisMac
            await flow.next()
            flow.boxName = "acme-api"
            await flow.next()
            return flow
        }
    }

    private static func name(_ step: FirstRunStep) -> String {
        switch step {
        case .welcome: "welcome"
        case .recoveryKey: "recovery-key"
        case .storage: "storage"
        case .firstBox: "first-box"
        case .gettingReady: "getting-ready"
        }
    }

    // MARK: Inspector and terminal

    /// The inspector: a box on this Mac with no server yet (Move To… says
    /// why it can't), its saves listed; saves that couldn't be loaded; and
    /// a box whose saves go to a server.
    /// Split view as the split-view board: claude on the left, grok over
    /// tests on the right, the claude pane focused; scripted terminals.
    private static func renderSplit(to dir: URL) async throws {
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let tabs = [
            TabInfo(id: "@1", name: "claude", active: true, program: "Claude Code",
                    waiting: TabQuestion(text: "Push fix/retries once the full suite passes?", secret: false),
                    typingAuthor: TabAuthor(name: "You", isYou: true)),
            TabInfo(id: "@2", name: "grok", active: false, program: "npm",
                    typingAuthor: TabAuthor(name: "Grok Bot", isYou: false)),
            TabInfo(id: "@3", name: "tests", active: false, program: "npm",
                    lastOutputAt: at.addingTimeInterval(-120), watchers: ["Claude Code"]),
            TabInfo(id: "@4", name: "dev", active: false),
        ]
        let provider = ScriptedTabProvider(seeds: [
            "@1": "$ claude --continue\n\u{25CF} The retry delay now takes the clock as a\n  parameter. Grok Bot is running the full suite\n  in the grok tab; the tests tab is watching.\n\n  Changed: api/retries.ts, api/retries.spec.ts\n  \u{2713} 40 of 40 runs pass locally\n",
            "@2": "$ git pull --rebase\nCurrent branch fix/retries is up to date.\n$ npm test -- --runInBand --coverage",
            "@3": "$ npm test -- --watch\n PASS  api/retries.spec.ts\n PASS  api/payments.spec.ts\n PASS  api/webhooks.spec.ts\n\nTests: 24 passed, 24 total\nTime:  3.1 s\n\nWatch usage: press a to run all, q to quit",
        ])
        // In a real, active-looking window captured by the window server:
        // the terminals draw on their layers, which a cached display leaves
        // out.
        let size = NSSize(width: 1280, height: 700)
        let before = NSApp.appearance
        defer { NSApp.appearance = before }
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            NSApp.appearance = NSAppearance(named: appearance)
            let tabsController = TabsController(box: "acme-api", daemon: SampleDaemon(savedAt: at))
            tabsController.receive(tabs)
            let split = SplitController(tabs: tabsController)
            split.size = size
            split.tabsChanged()
            await split.split(.right)
            await split.split(.down)
            split.focus(PaneID(1)) // the board's focused pane
            let window = ActiveLookingWindow(contentRect: NSRect(origin: .zero, size: size),
                                             styleMask: [.titled, .closable, .miniaturizable, .resizable],
                                             backing: .buffered, defer: false)
            window.isReleasedWhenClosed = false
            window.title = "acme-api"
            window.appearance = NSAppearance(named: appearance)
            window.contentView = NSHostingView(rootView: SplitPanes(split: split, provider: provider, generation: 0, box: "acme-api"))
            window.setContentSize(size)
            try await WindowCapture.capture(window, to: dir.appendingPathComponent("split-view-\(suffix).png"))
        }
    }

    private static func renderInspector(to dir: URL) throws {
        // Minutes before now, so "Last saved" reads as it would (past its
        // first minute) whenever the renders are made.
        let minute = (Date().timeIntervalSinceReferenceDate / 60).rounded(.down) * 60
        func ago(_ m: Int) -> Date { Date(timeIntervalSinceReferenceDate: minute - Double(m * 60)) }
        let saves = InspectorText.saveRows([
            SaveInfo(id: "a", time: ago(2), kind: "autosave", machine: "studio"),
            SaveInfo(id: "p", time: ago(29), kind: "point", machine: "studio", name: "before retry change"),
            SaveInfo(id: "o", time: ago(68), kind: "orphaned", machine: "studio"),
            SaveInfo(id: "r", time: ago(95), kind: "release", machine: "studio"),
        ])
        let tabs = InspectorText.tabRows([
            TabInfo(id: "@0", name: "claude", active: true, program: "claude"),
            TabInfo(id: "@1", name: "shell", active: false),
            TabInfo(id: "@2", name: "tests", active: false, program: "npm"),
        ])
        // Tall enough for every section at the board's type sizes.
        let size = NSSize(width: 300, height: 780)
        func inspector(_ content: InspectorContent, servers: [String]) -> some View {
            InspectorView(content: content, yourInitial: "P",
                          moveMenu: BoxMenuModel.moveSubmenu(busy: false, location: "this-mac", servers: servers), move: { _ in },
                          retryPackages: {}, revert: {}, scrolls: false)
                .frame(maxHeight: .infinity, alignment: .top)
                .background(.background)
        }
        try snapshot(inspector(InspectorContent(storagePlace: "this Mac", openedAt: ago(95), savedAt: ago(2),
                                                saves: saves, tabs: tabs), servers: []),
                     name: "inspector-this-mac", size: size, in: dir)
        try snapshot(inspector(InspectorContent(storagePlace: "this Mac", openedAt: ago(95), savedAt: ago(2),
                                                savesLoaded: false, savesFailed: true, tabs: tabs), servers: []),
                     name: "inspector-saves-failed", size: size, in: dir)
        try snapshot(inspector(InspectorContent(storagePlace: "your server", openedAt: ago(95), failedPackages: ["ripgrep"],
                                                savedAt: ago(2), saves: saves, tabs: tabs),
                               servers: ["portenv@test-server"]),
                     name: "inspector-saves-to-server", size: size, in: dir)
    }

    /// The terminal matching the system, with each ANSI colour on show.
    private static func sampleTerminal() -> NSView {
        let view = PaddedTerminal(margin: TerminalLayout.margin)
        view.terminal.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        let names = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"]
        var text = "\u{1b}[1mwork@acme-api\u{1b}[0m:\u{1b}[34m~/acme-api\u{1b}[0m$ git status --short\r\n"
        text += " \u{1b}[32mM\u{1b}[0m src/retry.ts\r\n \u{1b}[31mD\u{1b}[0m src/old.ts\r\n\u{1b}[31m??\u{1b}[0m notes.md\r\n\r\n"
        for (i, n) in names.enumerated() {
            text += "\u{1b}[3\(i)m" + n.padding(toLength: 9, withPad: " ", startingAt: 0) + "\u{1b}[0m "
            text += "\u{1b}[9\(i)mbright " + n + "\u{1b}[0m\r\n"
        }
        text += "\r\n\u{1b}[1mwork@acme-api\u{1b}[0m:\u{1b}[34m~/acme-api\u{1b}[0m$ "
        view.terminal.feed(text: text)
        return view
    }

    private static func snapshot(_ view: some View, name: String, size: NSSize = NSSize(width: 720, height: 520), in dir: URL) throws {
        // First run's screens must fit their window: content that doesn't
        // is squeezed, and SwiftUI cuts its text short with "…" (R-0216).
        if name.hasPrefix("first-run") { try requireFits(view, name: name, size: size) }
        // Drawn as in the key window of the active app, so the default
        // button is blue as a tester sees it (R-0153).
        try snapshot(view: { NSHostingView(rootView: view.frame(width: size.width, height: size.height).environment(\.isRendering, true)
                                .environment(\.controlActiveState, .key)) },
                     name: name, size: size, in: dir)
    }

    /// Fails when the view, at its own height with every text wrapped, is
    /// taller than the window: then some text would be cut short.
    private static func requireFits(_ view: some View, name: String, size: NSSize) throws {
        let host = NSHostingView(rootView: view.fixedSize(horizontal: false, vertical: true).frame(width: size.width)
            .environment(\.isRendering, true))
        let needed = host.fittingSize.height
        if needed > size.height + 0.5 {
            throw FirstRunError("\(name) needs \(Int(needed.rounded())) pt of height and has \(Int(size.height)): its text would be cut short")
        }
    }

    /// An alert as it shows: its own content, laid out by NSAlert, drawn in
    /// an active-looking window.
    private static func snapshot(alert make: () -> NSAlert, name: String, in dir: URL) throws {
        var alerts: [NSAlert] = [] // kept until drawn
        func content() -> NSView {
            let alert = make()
            alert.layout()
            alerts.append(alert)
            return alert.window.contentView ?? NSView()
        }
        let size = content().frame.size
        // The alert's own background is the window's, which the cached
        // display leaves out: drawn under it.
        try snapshot(view: content, name: name, size: size, in: dir, background: { host in
            var color = NSColor.windowBackgroundColor
            host.effectiveAppearance.performAsCurrentDrawingAppearance { color = NSColor.windowBackgroundColor.usingColorSpace(.sRGB) ?? color }
            return color
        })
    }

    /// Light and dark PNGs of a view, made fresh for each appearance.
    private static func snapshot(view make: () -> NSView, name: String, size: NSSize, in dir: URL,
                                 background: (NSView) -> NSColor? = { _ in nil }) throws {
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            let host = make()
            host.frame = NSRect(origin: .zero, size: size)
            // Drawn as the active window, so the default button is blue as
            // a tester sees it (R-0153), without taking focus.
            let window = ActiveLookingWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
            window.appearance = NSAppearance(named: appearance)
            window.contentView = host
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.2))
            guard let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds) else {
                throw FirstRunError("no bitmap for \(name)")
            }
            host.cacheDisplay(in: host.bounds, to: rep)
            var out = rep
            if let fill = background(host), let under = host.bitmapImageRepForCachingDisplay(in: host.bounds),
               let context = NSGraphicsContext(bitmapImageRep: under) {
                NSGraphicsContext.saveGraphicsState()
                NSGraphicsContext.current = context
                fill.setFill()
                host.bounds.fill()
                rep.draw(in: host.bounds, from: .zero, operation: .sourceOver, fraction: 1, respectFlipped: true, hints: nil)
                NSGraphicsContext.restoreGraphicsState()
                out = under
            }
            guard let png = out.representation(using: .png, properties: [:]) else {
                throw FirstRunError("no PNG for \(name)")
            }
            try png.write(to: dir.appendingPathComponent("\(name)-\(suffix).png"))
        }
    }

    /// One appearance of a SwiftUI view (renders whose content depends on
    /// the appearance, as the blocks' terminal palette does).
    private static func snapshotOne(_ view: AnyView, name: String, suffix: String, size: NSSize,
                                    appearance: NSAppearance.Name, in dir: URL) throws {
        let host = NSHostingView(rootView: view.frame(width: size.width, height: size.height))
        host.frame = NSRect(origin: .zero, size: size)
        let window = ActiveLookingWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: appearance)
        window.contentView = host
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))
        guard let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { throw FirstRunError("no bitmap for \(name)") }
        host.cacheDisplay(in: host.bounds, to: rep)
        guard let png = rep.representation(using: .png, properties: [:]) else { throw FirstRunError("no PNG for \(name)") }
        try png.write(to: dir.appendingPathComponent("\(name)-\(suffix).png"))
    }
}
