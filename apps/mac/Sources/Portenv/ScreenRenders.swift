// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// `Portenv --render-screens DIR` (dev builds): light and dark PNGs of each
/// first-run step and the box list, laid out in an offscreen window, for PRs
/// next to the mockups. Uses a sample flow: no keys, no boxes, no portenvd.
@MainActor
enum ScreenRenders {
    /// A sample recovery key, only ever drawn into renders.
    private struct Sample: FirstRunService {
        func makeRecoveryKey() async throws -> String { "K7QF-M2XA-9TRD-HV4P-W8LC-3NZE" }
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
                try snapshot(FirstRunView(flow: flow) { _ in }, name: "first-run-\(step.rawValue + 1)-\(name(step))", in: dir)
            }
            try snapshot(
                BoxListView(boxes: ["acme-api", "site", "notes"], notice: LaunchDecision.missing("demo"), open: { _ in }, newBox: {}),
                name: "box-list", in: dir
            )
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

    private static func snapshot(_ view: some View, name: String, in dir: URL) throws {
        for (suffix, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            let size = NSSize(width: 720, height: 520)
            let host = NSHostingView(rootView: view.frame(width: size.width, height: size.height))
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
            guard let png = rep.representation(using: .png, properties: [:]) else {
                throw FirstRunError("no PNG for \(name)")
            }
            try png.write(to: dir.appendingPathComponent("\(name)-\(suffix).png"))
        }
    }
}
