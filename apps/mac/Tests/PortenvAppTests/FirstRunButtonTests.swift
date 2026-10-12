// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// Makes keys and workspaces like the app would, without either.
private final class Service: FirstRunService, @unchecked Sendable {
    private let lock = NSLock()
    private var _made: [String] = []
    private var _keys = 0
    var made: [String] { lock.withLock { _made } }
    var keys: Int { lock.withLock { _keys } }
    /// Fails as "not built yet" (0.1.0-68's step 2).
    var notBuilt: String?

    func makeRecoveryKey() async throws -> String {
        if let notBuilt { throw FirstRunError(notBuilt, notBuilt: true) }
        let n = lock.withLock { _keys += 1; return _keys }
        return Array(repeating: "K\(n)AB", count: 6).joined(separator: "-")
    }

    func createBox(_ box: NewBox) async throws { lock.withLock { _made.append(box.name) } }
}

/// The frames first run reports for its buttons.
@MainActor
private final class Frames { var all: [String: CGRect] = [:] }

/// First run in a real window, its buttons clicked with mouse events at
/// their place on screen, as a person would (R-0126, R-0132). The tree
/// SwiftUI gives VoiceOver only exists for a real assistive client, so the
/// tests click; what's on screen is checked through the flow the view draws.
@MainActor
struct FirstRunButtonTests {
    private func settle() async {
        for _ in 0..<8 { try? await Task.sleep(for: .milliseconds(20)) }
    }

    private func host(_ flow: FirstRunFlow) -> (NSWindow, Frames) {
        _ = NSApplication.shared
        let frames = Frames()
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 720, height: 520),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: FirstRunView(flow: flow, opened: { _ in }, controlFrames: { frames.all = $0 }))
        window.makeKeyAndOrderFront(nil)
        return (window, frames)
    }

    /// Clicks the button named `name` (ControlFrames); false if it isn't
    /// on screen.
    @discardableResult
    private func click(_ name: String, in window: NSWindow, _ frames: Frames) async -> Bool {
        await settle()
        guard let f = frames.all[name], let content = window.contentView else { return false }
        // SwiftUI's frames are top-left; AppKit's window points bottom-left.
        let point = NSPoint(x: f.midX, y: content.bounds.height - f.midY)
        for type in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            guard let e = NSEvent.mouseEvent(with: type, location: point, modifierFlags: [],
                                             timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                                             context: nil, eventNumber: 0, clickCount: 1, pressure: 1) else { return false }
            window.sendEvent(e)
            await settle()
        }
        return true
    }

    /// Forwards through every step, Back to Welcome, then forwards again,
    /// pressing the real buttons. Every Continue moves on or puts its
    /// reason on screen, including the dimmed one.
    @Test func walkingThroughFirstRunWithItsButtons() async throws {
        let service = Service()
        let flow = FirstRunFlow(service: service)
        let (window, frames) = host(flow)

        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .recoveryKey)
        // Dimmed until ticked: the press still answers, on screen.
        let refused = flow.refusals
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .recoveryKey)
        #expect(flow.refusals == refused + 1)
        #expect(flow.reason == FirstRunText.tickSaved)
        flow.recoverySaved = true
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .storage)
        #expect(flow.storage == .thisMac)
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .firstBox)
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .firstBox)
        #expect(flow.reason == FirstRunText.typeName)

        for target in [FirstRunStep.storage, .recoveryKey, .welcome] {
            #expect(await click(ControlFrames.back, in: window, frames))
            #expect(flow.step == target)
        }
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .recoveryKey)
        #expect(service.keys == 1) // the same key after Back
        flow.recoverySaved = true
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(await click(ControlFrames.continue, in: window, frames))
        flow.boxName = "acme-api"
        #expect(await click(ControlFrames.continue, in: window, frames))
        #expect(flow.step == .gettingReady)
        #expect(service.made == ["acme-api"])
        window.close()
    }

    /// 0.1.0-68's step 2: a Continue that can't be done yet says so on
    /// screen, on every press.
    @Test func aStepNotBuiltSaysSoOnEveryPress() async throws {
        let service = Service()
        service.notBuilt = "Not ready yet."
        let flow = FirstRunFlow(service: service)
        let (window, frames) = host(flow)
        for n in 1...3 {
            #expect(await click(ControlFrames.continue, in: window, frames))
            #expect(flow.step == .welcome)
            #expect(flow.refusals == n)
            #expect(flow.reason == "Not ready yet.")
        }
        window.close()
    }
}
