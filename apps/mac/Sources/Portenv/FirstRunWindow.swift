// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

extension EnvironmentValues {
    /// Drawn for a render (`--render-screens`): nothing takes focus.
    @Entry var isRendering = false
}

/// Brings the window it's in forward as the key window once, when it first
/// appears in one (R-0153).
struct BringsWindowForward: NSViewRepresentable {
    let enabled: Bool

    /// How a window is brought forward (tests watch it: a test process
    /// can't become the active app).
    @MainActor static var bring: (NSWindow) -> Void = { window in
        NSApp.activate()
        window.makeKeyAndOrderFront(nil)
    }

    func makeNSView(context _: Context) -> NSView { Probe(enabled: enabled) }
    func updateNSView(_: NSView, context _: Context) {}

    final class Probe: NSView {
        let enabled: Bool
        private var done = false
        init(enabled: Bool) {
            self.enabled = enabled
            super.init(frame: .zero)
        }

        required init?(coder _: NSCoder) { nil }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard enabled, !done, let window else { return }
            done = true
            DispatchQueue.main.async { BringsWindowForward.bring(window) }
        }
    }
}

/// A window drawn as the active one (coloured traffic lights, the blue
/// default button) without being made key, so a render never takes focus.
final class ActiveLookingWindow: NSWindow {
    override var isKeyWindow: Bool { true }
    override var isMainWindow: Bool { true }
}

/// The recovery key sheet (ADR 0019, "When it runs out"): the pool is
/// empty, so New Workspace asks for the recovery key once. It's checked
/// against the workspaces here first; with nothing to check against, a new
/// key can be made instead.
struct RecoveryKeySheet: View {
    let refill: RecoveryRefill
    let cancel: () -> Void
    let done: () -> Void

    @State private var typed = ""
    @State private var newKey: String?
    @State private var saved = false
    @State private var busy = false
    @State private var line: String?
    @State private var offerNewKey = false

    init(refill: RecoveryRefill, cancel: @escaping () -> Void, done: @escaping () -> Void) {
        self.refill = refill
        self.cancel = cancel
        self.done = done
    }

    /// A state of the sheet, for renders.
    init(rendering refill: RecoveryRefill, typed: String = "", line: String? = nil, offerNewKey: Bool = false, newKey: String? = nil) {
        self.init(refill: refill, cancel: {}, done: {})
        _typed = State(initialValue: typed)
        _line = State(initialValue: line)
        _offerNewKey = State(initialValue: offerNewKey)
        _newKey = State(initialValue: newKey)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let newKey {
                Text(FirstRunText.recoveryTitle).font(.title2.bold()).accessibilityAddTraits(.isHeader)
                Text(FirstRunText.recoveryLine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                VStack(spacing: 12) {
                    Text(newKey).font(.system(size: 20, weight: .semibold, design: .monospaced))
                        .accessibilityLabel(A11y.recoveryKey(newKey))
                    HStack(spacing: 12) {
                        Button("Print…") { RecoveryKeyActions.print(newKey) }
                        Button("Copy") { RecoveryKeyActions.copy(newKey) }
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(20)
                .background(RoundedRectangle(cornerRadius: 12).fill(.quaternary.opacity(0.5)))
                Toggle(FirstRunText.recoverySavedCheck, isOn: $saved).toggleStyle(.checkbox)
            } else {
                Text(RefillText.title).font(.title2.bold()).accessibilityAddTraits(.isHeader)
                Text(BoxMaker.Failure.poolEmpty.errorDescription ?? "").foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                TextField(RefillText.field, text: $typed)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(.body, design: .monospaced))
                    .autocorrectionDisabled()
                    .accessibilityLabel(RefillText.field)
                    .onSubmit { Task { await primary() } }
            }
            if let line {
                Label(line, systemImage: "info.circle").fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if offerNewKey, newKey == nil {
                    Button(RefillText.makeNewKey) { makeNewKey() }
                }
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("Cancel", action: cancel).keyboardShortcut(.cancelAction)
                Button("Continue") { Task { await primary() } }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(busy)
            }
        }
        .padding(24)
        .frame(width: 520)
        .onChange(of: line) { _, new in if let new { Announce.high(new) } }
    }

    private func primary() async {
        guard !busy else { return }
        if newKey != nil {
            guard saved else {
                line = FirstRunText.tickSaved
                return
            }
            busy = true
            let outcome = await refill.newKeySaved()
            busy = false
            outcome == .refilled ? done() : (line = outcome.message)
            return
        }
        busy = true
        let outcome = await refill.refill(typed: typed)
        busy = false
        switch outcome {
        case .refilled:
            typed = ""
            done()
        case .nothingToCheck:
            offerNewKey = true
            line = outcome.message
        default:
            line = outcome.message
        }
    }

    private func makeNewKey() {
        do {
            newKey = try refill.makeNewKey()
            typed = ""
            line = nil
        } catch {
            line = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// The recovery key sheet's words.
enum RefillText {
    static let title = "Enter your recovery key"
    static let field = "Recovery key"
    static let makeNewKey = "Make a New Recovery Key"
}
