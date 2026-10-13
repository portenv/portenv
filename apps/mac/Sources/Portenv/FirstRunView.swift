// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

/// First run, reduced for Phases 1 and 2 (PLAN.md 1.5; the first-run-1 to 6
/// mockups, without sign-in or Portenv storage): a title, a line, the
/// step's content, then the page dots with Back and Continue.
struct FirstRunView: View {
    @Bindable var flow: FirstRunFlow
    /// The box made at the end, to open.
    let opened: (String) -> Void
    /// New Box… from the box list: Cancel, ⎋ and "Your boxes" go back to it.
    var cancel: (() -> Void)?
    /// Where Continue and Back are, in the window (top-left origin), for
    /// the tests that click them (PortenvAppTests). Nil in the app.
    var controlFrames: (([String: CGRect]) -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let cancel {
                Button(action: cancel) {
                    Label("Your workspaces", systemImage: "chevron.left")
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .padding(.bottom, 20)
                .accessibilityLabel("Back to your workspaces")
            }
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            if flow.step != .welcome {
                // Why Continue didn't move on, right above it (R-0126).
                refusal
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .padding(.bottom, 12)
                footer
            }
        }
        .padding(.horizontal, 64)
        .padding(.vertical, 48)
        .frame(minWidth: 720, minHeight: 520)
        .background(.background)
        .onChange(of: flow.made) { _, made in
            if let made { opened(made) }
        }
        .coordinateSpace(name: ControlFrames.space)
        .onPreferenceChange(ControlFrames.self) { frames in controlFrames?(frames) }
        // Every Continue that doesn't move on is heard, even with the same
        // words as the last one, at high priority (R-0126, R-0132).
        .onChange(of: flow.refusals) { _, _ in
            if let words = flow.reason { Announce.high(words) }
        }
    }

    /// Why Continue can't go on, or didn't: an error in red, or a notice (a
    /// step not built yet, or one that isn't complete). Shown for as long
    /// as it applies, so Continue is never dimmed without a reason on
    /// screen (R-0132).
    @ViewBuilder private var refusal: some View {
        if let error = flow.error {
            Label(error, systemImage: "exclamationmark.triangle")
                .foregroundStyle(.red)
                .fixedSize(horizontal: false, vertical: true)
                .multilineTextAlignment(.trailing)
                .accessibilityLabel("Problem: \(error)")
        } else if let reason = flow.reason {
            Label(reason, systemImage: "info.circle")
                .fixedSize(horizontal: false, vertical: true)
                .multilineTextAlignment(.trailing)
        }
    }

    @ViewBuilder private var content: some View {
        switch flow.step {
        case .welcome: welcome
        case .recoveryKey: recoveryKey
        case .storage: storage
        case .firstBox: firstBox
        case .gettingReady: gettingReady
        }
    }

    // MARK: Steps

    private var welcome: some View {
        VStack(spacing: 20) {
            // Portenv's own icon, with its own shape and shadow: no tile
            // behind it (R-0151). Dev lanes show their lettered icon.
            Image(nsImage: WelcomeArt.image)
                .resizable()
                .frame(width: WelcomeArt.size, height: WelcomeArt.size)
                .accessibilityHidden(true)
            Text(FirstRunText.welcomeTitle).font(.system(size: 30, weight: .bold))
                .accessibilityAddTraits(.isHeader)
            Text(FirstRunText.welcomeLine)
                .font(.title3)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 440)
            Button(FirstRunText.welcomeButton) { Task { await flow.next() } }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(.defaultAction)
                .disabled(flow.busy)
                .reportsFrame(ControlFrames.continue)
            // While the key is made (R-0132).
            if flow.busy {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text(FirstRunText.makingKey).foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
            }
            // Welcome has no footer: why Continue didn't move on goes under it.
            refusal
                .frame(maxWidth: 440)
            // Counting needs the update check, which tester builds don't
            // have yet: hidden until counting exists (R-0098, R-0106).
            if UsageCounting.shown {
                CountingChoice(countUsage: $flow.countUsage)
                    .frame(maxWidth: 440)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var recoveryKey: some View {
        heading(FirstRunText.recoveryTitle, FirstRunText.recoveryLine) {
            VStack(spacing: 16) {
                // Not selectable: it's copied only through Copy, concealed,
                // transient and cleared after 60 s (R-0132).
                Text(flow.recoveryKey ?? "")
                    .font(.system(size: 22, weight: .semibold, design: .monospaced))
                    .accessibilityLabel(A11y.recoveryKey(flow.recoveryKey ?? ""))
                HStack(spacing: 12) {
                    Button("Print…") { RecoveryKeyActions.print(flow.recoveryKey ?? "") }
                    Button("Copy") { RecoveryKeyActions.copy(flow.recoveryKey ?? "") }
                }
            }
            .frame(maxWidth: .infinity)
            .padding(24)
            .background(RoundedRectangle(cornerRadius: 12).fill(.quaternary.opacity(0.5)))
            Toggle(FirstRunText.recoverySavedCheck, isOn: $flow.recoverySaved)
                .toggleStyle(.checkbox)
                .padding(.top, 16)
            // A key shown in a launch that quit before finishing was never
            // used (R-0132, ADR 0019).
            if flow.keyShownBefore {
                Label(FirstRunText.replacedKey, systemImage: "info.circle")
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 12)
            }
        }
    }

    /// The first-run-4-storage-this-mac board: this Mac, chosen; a server
    /// and a bucket dimmed and marked LATER (R-0092, R-0132).
    private var storage: some View {
        heading(FirstRunText.storageTitle, FirstRunText.storageLine) {
            VStack(spacing: 12) {
                StorageRow(icon: "laptopcomputer", title: FirstRunText.storageThisMac, line: FirstRunText.thisMacOnly,
                           selected: flow.storage == .thisMac, later: false) { flow.storage = .thisMac }
                StorageRow(icon: "server.rack", title: FirstRunText.storageServer, line: FirstRunText.storageServerLine,
                           selected: false, later: true) {}
                StorageRow(icon: "cylinder", title: FirstRunText.storageBucket, line: FirstRunText.storageBucketLine,
                           selected: false, later: true) {}
            }
            .frame(maxWidth: 600)
        }
    }

    private var firstBox: some View {
        heading(flow.firstBoxTitle, flow.fromBoxList ? "" : FirstRunText.firstBoxLine) {
            VStack(alignment: .leading, spacing: 6) {
                let look = flow.nameField
                TextField("Name", text: $flow.boxName)
                    .textFieldStyle(.roundedBorder)
                    .frame(maxWidth: 360)
                    // While the name is invalid, a red border in place of
                    // the blue focus ring (R-0049, new-box-linked-invalid).
                    .focusEffectDisabled(look.showsError)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(.red, lineWidth: look.showsError ? 1.5 : 0))
                    // VoiceOver reads the problem as part of the field's
                    // description, and the rule as its help.
                    .accessibilityLabel(look.accessibilityDescription)
                    .accessibilityHint(look.accessibilityHelp ?? "")
                    .accessibilityIdentifier(NameFieldLook.identifier)
                    .onSubmit { Task { await flow.next() } }
                // One line under the field: the rule, or what's wrong (the
                // new-box boards; R-0032). The field already says it to
                // VoiceOver.
                Text(look.line)
                    .font(.callout)
                    .foregroundStyle(look.showsError ? Color.red : Color.secondary)
                    .accessibilityHidden(true)
            }
        }
    }

    private var gettingReady: some View {
        heading(FirstRunText.gettingReadyTitle, "") {
            HStack(spacing: 10) {
                ProgressView().controlSize(.small)
                Text("Making \(flow.boxName)…")
            }
            .accessibilityElement(children: .combine)
        }
    }

    // MARK: Pieces

    private func heading<Content: View>(_ title: String, _ line: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(title).font(.system(size: 26, weight: .bold))
                .accessibilityAddTraits(.isHeader)
            if !line.isEmpty {
                Text(line)
                    .font(.title3)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 600, alignment: .leading)
                    .padding(.bottom, 12)
            }
            content()
        }
    }

    private var footer: some View {
        HStack {
            if flow.showsStepDots {
                StepDots(steps: flow.steps, current: flow.step)
            }
            Spacer()
            if flow.step != .gettingReady {
                if let cancel {
                    Button("Cancel", action: cancel)
                        .controlSize(.large)
                        .keyboardShortcut(.cancelAction)
                } else if flow.canGoBack {
                    Button("Back") { flow.back() }
                        .controlSize(.large)
                        .keyboardShortcut(.cancelAction)
                        .reportsFrame(ControlFrames.back)
                }
                // Filled only when it can be pressed; otherwise dimmed and
                // disabled, not only inert (R-0049).
                if flow.busy {
                    ProgressView().controlSize(.small).padding(.trailing, 4)
                }
                // Dimmed while the step isn't complete, but a click still
                // reaches next(), which says why (R-0049, R-0132).
                Button(flow.primaryButton) { Task { await flow.next() } }
                    .buttonStyle(.borderedProminent)
                    .tint(flow.primaryProminent ? Color.accentColor : Color.secondary.opacity(0.35))
                    .controlSize(.large)
                    .keyboardShortcut(.defaultAction)
                    .disabled(flow.busy)
                    .accessibilityHint(flow.whyNot ?? "")
                    .reportsFrame(ControlFrames.continue)
            }
        }
    }
}

/// One storage choice, as a card (the board). A choice that comes later is
/// dimmed, marked LATER and can't be chosen.
struct StorageRow: View {
    let icon: String
    let title: String
    let line: String
    let selected: Bool
    let later: Bool
    let choose: () -> Void

    var body: some View {
        Button(action: choose) {
            HStack(alignment: .center, spacing: 16) {
                Image(systemName: icon).font(.title2).frame(width: 32)
                VStack(alignment: .leading, spacing: 4) {
                    Text(title).font(.headline)
                    Text(line).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 12)
                if later {
                    Text(FirstRunText.later).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
            .background(RoundedRectangle(cornerRadius: 12).fill(selected ? Color.accentColor.opacity(0.12) : Color.clear))
            .overlay(RoundedRectangle(cornerRadius: 12)
                .stroke(selected ? Color.accentColor : Color.secondary.opacity(0.3), lineWidth: selected ? 2 : 1))
        }
        .buttonStyle(.plain)
        .disabled(later)
        .opacity(later ? 0.5 : 1)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(title). \(line)")
        .accessibilityValue(later ? "Later" : "")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// The frames of first run's buttons, by name, for the tests that click them.
struct ControlFrames: PreferenceKey {
    static let `continue` = "continue"
    static let back = "back"
    /// The whole view, top-left origin.
    static let space = "first-run"
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue()) { $1 }
    }
}

extension View {
    func reportsFrame(_ name: String) -> some View {
        background(GeometryReader { g in Color.clear.preference(key: ControlFrames.self, value: [name: g.frame(in: .named(ControlFrames.space))]) })
    }
}

/// Announcements VoiceOver says at once, over what it's reading (R-0132).
enum Announce {
    @MainActor
    static func high(_ words: String) {
        NSAccessibility.post(element: NSApp.mainWindow as Any? ?? NSApp as Any, notification: .announcementRequested,
                             userInfo: [.announcement: words, .priority: NSAccessibilityPriorityLevel.high.rawValue])
    }
}

/// Welcome's picture: the application icon itself (R-0151).
enum WelcomeArt {
    static let size: CGFloat = 112
    @MainActor static var image: NSImage { PortenvIcon.image }
}

/// The page dots under first run (the mockups'), read as "Step 3 of 5".
struct StepDots: View {
    let steps: [FirstRunStep]
    let current: FirstRunStep

    var body: some View {
        let index = steps.firstIndex(of: current) ?? 0
        HStack(spacing: 8) {
            ForEach(steps.indices, id: \.self) { i in
                Circle()
                    .fill(i == index ? Color.primary : Color.secondary.opacity(0.4))
                    .frame(width: 7, height: 7)
            }
        }
        .accessibilityElement()
        .accessibilityLabel("Step \(index + 1) of \(steps.count)")
    }
}

/// "Count me in usage numbers", off until ticked (R-0126), with the exact
/// fields one click away (PLAN.md 1.8; point 6 of the CNIL criteria).
struct CountingChoice: View {
    @Binding var countUsage: Bool
    @State private var showFields = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Toggle(FirstRunText.countMeIn, isOn: $countUsage)
                .toggleStyle(.checkbox)
            DisclosureGroup(FirstRunText.whatsCounted, isExpanded: $showFields) {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(FirstRunText.countedFields, id: \.self) { field in
                        Text("• " + field).fixedSize(horizontal: false, vertical: true)
                    }
                    Text(FirstRunText.neverCounted).fixedSize(horizontal: false, vertical: true)
                    Text(FirstRunText.turnOff).fixedSize(horizontal: false, vertical: true)
                }
                .font(.callout)
                .foregroundStyle(.secondary)
                .padding(.top, 4)
            }
        }
    }
}

/// Copy and Print for the recovery key. Copy marks the pasteboard as
/// concealed and transient, so clipboard managers that honour it don’t keep
/// the key, and clears it after 60 s if it’s still there (ConcealedCopy).
enum RecoveryKeyActions {
    @MainActor
    static func copy(_ key: String) {
        ConcealedCopy.copyThenClear(key)
    }

    @MainActor
    static func print(_ key: String) {
        let text = NSTextView(frame: NSRect(x: 0, y: 0, width: 468, height: 200))
        text.string = "Portenv recovery key\n\n\(key)\n\n\(FirstRunText.recoveryLine)"
        text.font = .monospacedSystemFont(ofSize: 14, weight: .regular)
        NSPrintOperation(view: text).run()
    }
}
