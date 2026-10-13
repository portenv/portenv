// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// First run's window comes forward as the key window when it shows, so its
/// default button is blue and Return reaches it (R-0153: the owner's
/// Continue looked grey). A render never does: it mustn't take focus.
@MainActor
@Suite(.serialized)
struct FirstRunWindowTests {
    private struct Service: FirstRunService {
        func makeRecoveryKey() async throws -> String { "" }
        func createBox(_: NewBox) async throws {}
    }

    private func settle() async {
        for _ in 0..<8 { try? await Task.sleep(for: .milliseconds(20)) }
    }

    /// Shows first run in a window ordered behind another, and returns the
    /// windows it asked to bring forward.
    private func show(rendering: Bool) async -> (NSWindow, [NSWindow]) {
        _ = NSApplication.shared
        var brought: [NSWindow] = []
        let saved = BringsWindowForward.bring
        BringsWindowForward.bring = { brought.append($0) }
        defer { BringsWindowForward.bring = saved }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 720, height: 520), styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: FirstRunView(flow: FirstRunFlow(service: Service()), opened: { _ in })
            .environment(\.isRendering, rendering))
        window.orderBack(nil) // shown, but not key
        await settle()
        window.close()
        // Other suites' windows may be shown meanwhile: only this one counts.
        return (window, brought.filter { $0 === window })
    }

    @Test func firstRunBringsItsWindowForward() async {
        let (window, brought) = await show(rendering: false)
        #expect(brought.count == 1)
        #expect(brought.first === window)
    }

    @Test func aRenderNeverTakesFocus() async {
        let (_, brought) = await show(rendering: true)
        #expect(brought.isEmpty)
    }

    /// The default bring activates the app and makes the window key.
    @Test func theDefaultBringActivatesAndMakesKey() throws {
        let source = try String(contentsOf: URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Sources/Portenv/FirstRunWindow.swift"), encoding: .utf8)
        #expect(source.contains("NSApp.activate()\n        window.makeKeyAndOrderFront(nil)"))
    }
}
