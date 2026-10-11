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
    /// A sample recovery key, only ever drawn into renders; built from
    /// groups so no key-shaped literal is in the source.
    private struct Sample: FirstRunService {
        func makeRecoveryKey() async throws -> String { ["SAMP", "LE00", "KEY0", "ONLY", "FOR0", "DOCS"].joined(separator: "-") }
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
                try snapshot(FirstRunView(flow: flow, opened: { _ in }), name: "first-run-\(step.rawValue + 1)-\(name(step))", in: dir)
            }
            // New Box… from the box list (the new-box boards).
            let newBox = FirstRunFlow(service: Sample(), at: .firstBox, existing: ["acme-api"], fromBoxList: true)
            newBox.boxName = "site"
            try snapshot(FirstRunView(flow: newBox, opened: { _ in }, cancel: {}), name: "new-box", in: dir)
            let invalid = FirstRunFlow(service: Sample(), at: .firstBox, existing: ["acme-api"], fromBoxList: true)
            invalid.boxName = "-api"
            try snapshot(FirstRunView(flow: invalid, opened: { _ in }, cancel: {}), name: "new-box-invalid", in: dir)
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
            try renderInspector(to: dir)
            // The terminal paints its background on its layer, which a
            // cached display leaves out: drawn under it here.
            try snapshot(view: { sampleTerminal() }, name: "terminal", size: NSSize(width: 900, height: 452), in: dir,
                         background: { ($0 as? PaddedTerminal)?.terminal.nativeBackgroundColor })
            return 0
        } catch {
            FileHandle.standardError.write(Data("render-screens: \(error)\n".utf8))
            return 1
        }
    }

    private static func sampleFlow(at step: FirstRunStep) async -> FirstRunFlow {
        switch step {
        case .welcome, .protection:
            return FirstRunFlow(service: Sample(), at: step)
        case .recoveryKey:
            let flow = FirstRunFlow(service: Sample(), at: .protection)
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
        case .protection: "protection"
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
        let size = NSSize(width: 300, height: 600)
        func inspector(_ content: InspectorContent, targets: [(String, String)]) -> some View {
            InspectorView(content: content, yourInitial: "P", moveTargets: targets, move: { _ in },
                          retryPackages: {}, revert: {}, scrolls: false)
                .frame(maxHeight: .infinity, alignment: .top)
                .background(.background)
        }
        try snapshot(inspector(InspectorContent(storagePlace: "this Mac", openedAt: ago(95), savedAt: ago(2),
                                                saves: saves, tabs: tabs), targets: []),
                     name: "inspector-this-mac", size: size, in: dir)
        try snapshot(inspector(InspectorContent(storagePlace: "this Mac", openedAt: ago(95), savedAt: ago(2),
                                                savesLoaded: false, savesFailed: true, tabs: tabs), targets: []),
                     name: "inspector-saves-failed", size: size, in: dir)
        try snapshot(inspector(InspectorContent(storagePlace: "your server", openedAt: ago(95), failedPackages: ["ripgrep"],
                                                savedAt: ago(2), saves: saves, tabs: tabs),
                               targets: [("test-server", "portenv@test-server")]),
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
        try snapshot(view: { NSHostingView(rootView: view.frame(width: size.width, height: size.height)) }, name: name, size: size, in: dir)
    }

    /// Light and dark PNGs of a view, made fresh for each appearance.
    private static func snapshot(view make: () -> NSView, name: String, size: NSSize, in dir: URL,
                                 background: (NSView) -> NSColor? = { _ in nil }) throws {
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            let host = make()
            host.frame = NSRect(origin: .zero, size: size)
            let window = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
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
}
