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

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            if let error = flow.error {
                Text(error)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.bottom, 12)
                    .accessibilityLabel("Problem: \(error)")
            }
            if flow.step != .welcome {
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
    }

    @ViewBuilder private var content: some View {
        switch flow.step {
        case .welcome: welcome
        case .protection: protection
        case .recoveryKey: recoveryKey
        case .storage: storage
        case .firstBox: firstBox
        case .gettingReady: gettingReady
        }
    }

    // MARK: Steps

    private var welcome: some View {
        VStack(spacing: 20) {
            Image(systemName: "house")
                .font(.system(size: 44, weight: .regular))
                .frame(width: 84, height: 84)
                .background(RoundedRectangle(cornerRadius: 20).fill(.quaternary))
                .accessibilityHidden(true)
            Text(FirstRunText.welcomeTitle).font(.system(size: 30, weight: .bold))
            Text(FirstRunText.welcomeLine)
                .font(.title3)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 440)
            Button(FirstRunText.welcomeButton) { Task { await flow.next() } }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(.defaultAction)
            CountingChoice(countUsage: $flow.countUsage)
                .frame(maxWidth: 440)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var protection: some View {
        heading(FirstRunText.protectionTitle, FirstRunText.protectionLine) {
            Label("Only you", systemImage: "lock")
                .font(.headline)
                .accessibilityLabel("Protection: only you")
        }
    }

    private var recoveryKey: some View {
        heading(FirstRunText.recoveryTitle, FirstRunText.recoveryLine) {
            VStack(spacing: 16) {
                Text(flow.recoveryKey ?? "")
                    .font(.system(size: 22, weight: .semibold, design: .monospaced))
                    .textSelection(.enabled)
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
        }
    }

    private var storage: some View {
        heading(FirstRunText.storageTitle, "") {
            VStack(alignment: .leading, spacing: 14) {
                Picker("Where should your saves go?", selection: $flow.storage) {
                    Text(FirstRunText.storageServer).tag(StorageChoice?.some(.server))
                    Text(FirstRunText.storageBucket).tag(StorageChoice?.some(.bucket))
                    Text(FirstRunText.storageThisMac).tag(StorageChoice?.some(.thisMac))
                }
                .pickerStyle(.radioGroup)
                .labelsHidden()
                switch flow.storage {
                case .server:
                    TextField("user@server", text: $flow.serverAddress)
                        .textFieldStyle(.roundedBorder)
                        .frame(maxWidth: 360)
                        .accessibilityLabel("Server address")
                case .bucket:
                    TextField("s3:https://…/bucket", text: $flow.bucketURL)
                        .textFieldStyle(.roundedBorder)
                        .frame(maxWidth: 360)
                        .accessibilityLabel("Bucket address")
                case .thisMac:
                    Text(FirstRunText.thisMacOnly)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: 520, alignment: .leading)
                case nil:
                    EmptyView()
                }
            }
        }
    }

    private var firstBox: some View {
        heading(FirstRunText.firstBoxTitle, FirstRunText.firstBoxLine) {
            TextField("Name", text: $flow.boxName)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 360)
                .accessibilityLabel("Box name")
                .onSubmit { Task { await flow.next() } }
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
            StepDots(steps: flow.steps, current: flow.step)
            Spacer()
            if flow.step != .gettingReady {
                if flow.canGoBack {
                    Button("Back") { flow.back() }
                        .controlSize(.large)
                        .keyboardShortcut(.cancelAction)
                }
                Button("Continue") { Task { await flow.next() } }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .keyboardShortcut(.defaultAction)
                    .disabled(!flow.canContinue)
            }
        }
    }
}

/// The page dots under first run (the mockups'), read as "Step 3 of 6".
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

/// "Count me in usage numbers", already on, with the exact fields one
/// click away (PLAN.md 1.8; point 6 of the CNIL criteria).
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
