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
    /// Welcome checks Docker Desktop through portenvd (off for renders).
    var followsDocker = true
    /// Where Continue and Back are, in the window (top-left origin), for
    /// the tests that click them (PortenvAppTests). Nil in the app.
    var controlFrames: (([String: CGRect]) -> Void)?
    /// Refills the pool when it runs out (the recovery key sheet).
    var refill: RecoveryRefill?
    @Environment(\.isRendering) private var isRendering

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
        // Every step, Team and Review too, lays out within the window's
        // minimum: a larger minimum here only cut the step off on both
        // sides in a smaller window (the Team step was 880 pt in 720).
        .frame(minWidth: 720, minHeight: 520)
        .background(.background)
        // The board's size, centred on the display Portenv was launched
        // from, the first time this window shows first run (R-0186).
        .background(FirstRunWindowPlacer())
        .onChange(of: flow.made) { _, made in
            if let made { opened(made) }
        }
        .coordinateSpace(name: ControlFrames.space)
        .onPreferenceChange(ControlFrames.self) { frames in controlFrames?(frames) }
        // In front and key when it shows, so Continue is the blue default
        // and Return reaches it (R-0153). Renders never take focus.
        .background(BringsWindowForward(enabled: !isRendering))
        .sheet(isPresented: $flow.needsRecoveryKey) {
            if let refill {
                RecoveryKeySheet(refill: refill, cancel: { flow.needsRecoveryKey = false }) {
                    Task { await flow.recoveryKeyRefilled() }
                }
            }
        }
        // Every Continue that doesn't move on is heard, even with the same
        // words as the last one, at high priority (R-0126, R-0132).
        .onChange(of: flow.refusals) { _, _ in
            if let words = flow.reason { Announce.high(words) }
        }
        .onChange(of: flow.keychainWaiting) { _, on in
            if on { Announce.high("\(KeychainWait.line). \(KeychainWait.detail)") }
        }
    }

    /// Why Continue can't go on, or didn't: an error in red, or a notice (a
    /// step not built yet, or one that isn't complete). Shown for as long
    /// as it applies, so Continue is never dimmed without a reason on
    /// screen (R-0132).
    @ViewBuilder private var refusal: some View {
        if let wait = flow.waitLine {
            // A Keychain prompt may be up, maybe behind other windows
            // (GUIDELINES §3.1).
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                ProgressView().controlSize(.small)
                VStack(alignment: .trailing, spacing: 2) {
                    Text(wait)
                    Text(KeychainWait.detail).foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
        } else if let error = flow.error {
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
        case .project: projectStep
        case .team: teamStep
        case .review: reviewStep
        case .gettingReady: gettingReady
        }
    }

    // MARK: Steps

    private var welcome: some View {
        // With a notice under it, Welcome tightens so it all fits the window,
        // never cutting the tagline short (R-0216).
        let notice = flow.daemonGone || flow.docker?.card != nil
        return VStack(spacing: notice ? 14 : 20) {
            // Portenv's own icon, with its own shape and shadow: no tile
            // behind it (R-0151). Dev lanes show their lettered icon.
            Image(nsImage: WelcomeArt.image)
                .resizable()
                .frame(width: notice ? WelcomeArt.compactSize : WelcomeArt.size, height: notice ? WelcomeArt.compactSize : WelcomeArt.size)
                .accessibilityHidden(true)
            Text(FirstRunText.welcomeTitle).font(.system(size: 30, weight: .bold))
                .accessibilityAddTraits(.isHeader)
            Text(FirstRunText.welcomeLine)
                .font(.title3)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                // Wraps in every state: never cut short when a card shows (R-0216).
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: 440)
            // portenvd didn't answer the check: say so, with Try Again
            // (R-0110). Otherwise, Docker Desktop isn't running or isn't
            // installed (R-0098): a card until it answers, checked again
            // every 2 s.
            dockerNotice
            // Continue waits for Docker; dimmed as well as disabled (R-0049).
            Button(FirstRunText.welcomeButton) { Task { await flow.next() } }
                .buttonStyle(.borderedProminent)
                .tint(flow.primaryProminent ? Color.accentColor : Color.secondary.opacity(0.35))
                .controlSize(.large)
                .keyboardShortcut(.defaultAction)
                // Disabled until Docker Desktop answers, saying why (R-0116).
                .disabled(flow.busy || !flow.canContinue)
                .accessibilityHint(flow.continueHint ?? "")
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
        .task {
            guard followsDocker else { return }
            await flow.followDocker(check: { await DockerDesktop.status() })
        }
        // The card is a status region: VoiceOver hears it appear and
        // change, and hears it go only if one was shown (R-0110).
        .onChange(of: flow.docker) { old, new in
            if let words = DockerStatus.announcement(from: old, to: new) {
                AccessibilityNotification.Announcement(words).post()
            }
        }
        // The notice coming, or going with a card still there (R-0116).
        .onChange(of: flow.daemonGone) { _, _ in
            if let words = flow.serviceNoticeAnnouncement {
                AccessibilityNotification.Announcement(words).post()
            }
        }
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
    /// dimmed and marked LATER until 2.9 (R-0092, R-0132). No bucket: the
    /// owner dropped it as too technical (R-0196).
    private var storage: some View {
        heading(FirstRunText.storageTitle, FirstRunText.storageLine) {
            VStack(spacing: 12) {
                StorageRow(icon: "laptopcomputer", title: FirstRunText.storageThisMac, line: FirstRunText.thisMacOnly,
                           selected: flow.storage == .thisMac, later: false) { flow.storage = .thisMac }
                StorageRow(icon: "server.rack", title: FirstRunText.storageServer, line: FirstRunText.storageServerLine,
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
                // Docker Desktop ready before the first workspace (R-0211).
                dockerNotice
                    .padding(.top, 12)
            }
        }
        .task {
            guard followsDocker else { return }
            await flow.followDocker(check: { await DockerDesktop.status() })
        }
    }

    /// portenvd didn't answer the Docker check: say so, with Try Again
    /// (R-0110). Otherwise, Docker Desktop isn't ready: a card until it
    /// is, checked again every 2 s (R-0098, R-0211).
    @ViewBuilder private var dockerNotice: some View {
        if flow.daemonGone {
            VStack(spacing: 10) {
                Text(FirstRunText.serviceNotAnswering)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                Button(InspectorText.tryAgain) {
                    Task { await flow.checkDocker { await DockerDesktop.status() } }
                }
                .disabled(flow.checkingDocker)
            }
            .frame(maxWidth: 440)
        } else if let card = flow.docker?.card {
            DockerCardView(card: card) { DockerDesktop.perform(card.action) }
                .frame(maxWidth: 460)
        }
    }

    private var gettingReady: some View {
        heading(FirstRunText.gettingReadyTitle, "") {
            HStack(spacing: 10) {
                ProgressView().controlSize(.small)
                Text("Making \(flow.boxName)…")
            }
            .accessibilityElement(children: .combine)
            // With a team (ADR 0025 §5): nothing starts by itself.
            if !flow.team.included.isEmpty, flow.team.template != nil {
                VStack(alignment: .leading, spacing: 4) {
                    Text(FirstRunText.nothingStarts).fontWeight(.semibold)
                    Text(FirstRunText.signsIn).foregroundStyle(.secondary)
                }
                .padding(.top, 16)
            }
        }
    }

    /// The step names while New workspace has a Team step (ADR 0025 §1).
    @ViewBuilder private var stepNames: some View {
        if flow.hasTeamStep, [.firstBox, .project, .team, .review].contains(flow.step) {
            StepNames(steps: [("Name", flow.step == .firstBox), (ProjectText.title, flow.step == .project),
                              (NewTeamText.teamTitle, flow.step == .team), (NewTeamText.reviewTitle, flow.step == .review)])
        }
    }

    private var projectStep: some View {
        heading(ProjectText.title, ProjectText.line) {
            ProjectStepView(flow: flow)
        }
    }

    private var teamStep: some View {
        heading(NewTeamText.teamTitle, NewTeamText.teamLine) {
            // The cards and every agent's row, scrolled as one only when the
            // window can't hold them all.
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 0) {
                    TeamStepView(team: flow.team)
                    dockerNotice.padding(.top, 8)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .task {
            guard followsDocker else { return }
            await flow.followDocker(check: { await DockerDesktop.status() })
        }
    }

    private var reviewStep: some View {
        heading(NewTeamText.reviewTitle, "") {
            ReviewStepView(team: flow.team, project: flow.project)
            Text(FirstRunText.teamStartLine).font(.callout).foregroundStyle(.secondary).padding(.top, 8)
            dockerNotice.padding(.top, 8)
        }
    }

    // MARK: Pieces

    private func heading<Content: View>(_ title: String, _ line: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            stepNames
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
                // New workspace's later steps have Back as well as Cancel
                // (ADR 0025 §1).
                if cancel != nil, flow.canGoBack {
                    Button("Back") { flow.back() }
                        .controlSize(.large)
                        .reportsFrame(ControlFrames.back)
                }
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
                    // Review and approve has no default button (ADR 0025 §4).
                    .keyboardShortcut(flow.step == .review ? nil : KeyboardShortcut.defaultAction)
                    .disabled(flow.busy || flow.keychainWaiting)
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
    /// With a Docker or service notice under it.
    static let compactSize: CGFloat = 88
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

/// Places first run's window once: the board's size, centred on the display
/// under the pointer when Portenv launched (WindowPlacement.firstRun).
private struct FirstRunWindowPlacer: NSViewRepresentable {
    func makeNSView(context _: Context) -> NSView { Placer() }
    func updateNSView(_: NSView, context _: Context) {}

    final class Placer: NSView {
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard let window, !FirstRunLaunch.placed.contains(ObjectIdentifier(window)) else { return }
            FirstRunLaunch.placed.insert(ObjectIdentifier(window))
            let screens = NSScreen.screens.map(\.visibleFrame)
            guard let content = WindowPlacement.firstRun(pointer: FirstRunLaunch.pointer, screens: screens) else { return }
            let frame = window.frameRect(forContentRect: NSRect(origin: .zero, size: content.size))
            window.setFrame(NSRect(x: content.midX - frame.width / 2, y: content.midY - frame.height / 2,
                                   width: frame.width, height: frame.height), display: true)
        }
    }
}

/// Where the pointer was when Portenv launched, and the windows already
/// placed for first run.
@MainActor
enum FirstRunLaunch {
    static var pointer = NSEvent.mouseLocation
    static var placed: Set<ObjectIdentifier> = []
}
