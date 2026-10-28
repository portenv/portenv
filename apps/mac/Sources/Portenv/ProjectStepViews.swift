// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import SwiftUI

// New workspace's Project step (ADR 0025 §2) and Sign in with GitHub
// (R-0390, R-0391). The repository is cloned when the workspace first
// opens, fetched on this Mac: the sign-in never enters the workspace.

struct ProjectStepView: View {
    @Bindable var flow: FirstRunFlow
    @State private var signIn = GitHubSignIn()
    @State private var signingIn = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            CardRows {
                TemplateCard(title: ProjectText.empty, line: ProjectText.emptyLine, selected: flow.projectKind == .empty) {
                    flow.projectKind = .empty
                }
                TemplateCard(title: ProjectText.github, line: ProjectText.githubLine, selected: flow.projectKind == .github) {
                    flow.projectKind = .github
                }
            }
            .frame(maxWidth: 600)
            if flow.projectKind == .github {
                VStack(alignment: .leading, spacing: 6) {
                    TextField(ProjectText.urlPlaceholder, text: $flow.githubURL)
                        .textFieldStyle(.roundedBorder)
                        .frame(maxWidth: 460)
                        .autocorrectionDisabled()
                        .accessibilityLabel(ProjectText.urlField)
                        .onSubmit { Task { await flow.next() } }
                    // What GitHub says about it, asked on this Mac once the
                    // address is valid (finding 10).
                    RepoAccessRow(flow: flow) { signingIn = true }
                    if !flow.accessWantsSignIn {
                        GitHubAccountRow(signIn: signIn) { signingIn = true }
                    }
                }
            }
        }
        // Asked again after a pause in typing, and when the sign-in changes.
        .task(id: "\(flow.accessKey)\n\(signIn.state)") { await flow.checkAccess() }
        .onChange(of: flow.accessLine) { _, line in
            if let line, line != ProjectText.checking { Announce.high(line) }
        }
        .sheet(isPresented: $signingIn) {
            GitHubSignInSheet(signIn: signIn) { signingIn = false }
        }
    }
}

/// The access check's line under the address, and Sign in with GitHub…
/// when that would fix it.
struct RepoAccessRow: View {
    @Bindable var flow: FirstRunFlow
    let signIn: () -> Void

    var body: some View {
        if let line = flow.accessLine {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                if flow.access == nil {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: icon).foregroundStyle(tint).accessibilityHidden(true)
                }
                Text(verbatim: line).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .font(.callout)
            .frame(maxWidth: 460, alignment: .leading)
            .accessibilityElement(children: .combine)
            if flow.accessWantsSignIn {
                Button(GitHubSignInText.button, action: signIn)
                    .buttonStyle(.borderedProminent)
            }
        }
    }

    private var icon: String {
        switch flow.access {
        case .publicRepo?, .reachable?: "checkmark.circle"
        case .couldNotCheck?: "questionmark.circle"
        default: "exclamationmark.triangle"
        }
    }

    private var tint: SwiftUI.Color {
        switch flow.access {
        case .publicRepo?, .reachable?: .green
        case .couldNotCheck?: .secondary
        default: .orange
        }
    }
}

/// Who's signed in to GitHub, with Sign Out; or Sign in with GitHub….
struct GitHubAccountRow: View {
    let signIn: GitHubSignIn
    let start: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if case .signedIn(let login) = signIn.state {
                Text(GitHubSignInText.signedIn(login)).foregroundStyle(.secondary)
                Button(GitHubSignInText.signOut) { Task { await signIn.signOut() } }
                    .buttonStyle(.link)
            } else {
                Text(ProjectText.privateHint).foregroundStyle(.secondary)
                Button(GitHubSignInText.button, action: start).buttonStyle(.link)
            }
        }
        .font(.callout)
    }
}

/// GitHub's device flow: the code, Open GitHub, then waiting; what the
/// access is, in the owner's words (R-0391).
struct GitHubSignInSheet: View {
    let signIn: GitHubSignIn
    let done: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(GitHubSignInText.title).font(.title2.bold()).accessibilityAddTraits(.isHeader)
            Text(GitHubSignInText.access).fixedSize(horizontal: false, vertical: true)
            switch signIn.state {
            case .waiting:
                if let code = signIn.userCode {
                    Text(GitHubSignInText.enterCode)
                    Text(verbatim: code).font(.system(size: 26, weight: .semibold, design: .monospaced))
                        .textSelection(.enabled)
                        .accessibilityLabel("Code \(code.map(String.init).joined(separator: " "))")
                    HStack {
                        Button(GitHubSignInText.copyCode) {
                            NSPasteboard.general.clearContents()
                            NSPasteboard.general.setString(code, forType: .string)
                        }
                        if let url = signIn.verificationURL {
                            Button(GitHubSignInText.openGitHub) { NSWorkspace.shared.open(url) }
                        }
                    }
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text(GitHubSignInText.waiting).foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }
            case .signedIn(let login):
                Label(GitHubSignInText.signedIn(login), systemImage: "checkmark.circle")
            case .failed(let line):
                Label(line, systemImage: "exclamationmark.triangle").foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            case .signedOut:
                ProgressView().controlSize(.small)
            }
            HStack {
                Spacer()
                if case .failed = signIn.state {
                    Button(ProjectText.tryAgain) { Task { await run() } }
                }
                Button(isSignedIn ? "Done" : "Cancel", action: done)
                    .keyboardShortcut(isSignedIn ? .defaultAction : .cancelAction)
            }
        }
        .padding(24)
        .frame(width: 480)
        .task { if case .signedIn = signIn.state {} else { await run() } }
        .onChange(of: signIn.state) { _, s in
            if case .failed(let line) = s { Announce.high(line) }
        }
    }

    private var isSignedIn: Bool {
        if case .signedIn = signIn.state { return true }
        return false
    }

    private func run() async {
        await signIn.start()
        await signIn.waitForApproval()
    }
}

/// The window's line while the kept project is brought in, or when it
/// couldn't be: Try Again, and Sign in with GitHub… when that would fix it.
struct SetupBanner: View {
    @Bindable var team: TeamController
    @State private var signIn = GitHubSignIn()
    @State private var signingIn = false

    var body: some View {
        // One container, so the sign-in sheet stays up while the line
        // turns into the clone's progress (R-0409).
        VStack(spacing: 0) {
            if let problem = team.setupProblem {
                HStack(spacing: 10) {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange).accessibilityHidden(true)
                    Text(verbatim: problem.line).fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 8)
                    if problem.needsSignIn {
                        Button(GitHubSignInText.button) { signingIn = true }
                    }
                    Button(ProjectText.tryAgain) { Task { await team.retrySetup() } }
                }
                .font(.callout)
                .padding(10)
                .background(SwiftUI.Color.orange.opacity(0.12))
                .accessibilityElement(children: .contain)
                .onAppear { Announce.high(problem.line) }
            } else if let line = team.progressLine {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text(verbatim: line).foregroundStyle(.secondary)
                }
                .font(.callout)
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityElement(children: .combine)
                .onAppear { Announce.high(line) }
            }
        }
        .sheet(isPresented: $signingIn) {
            GitHubSignInSheet(signIn: signIn) { signingIn = false }
        }
        // Signing in from the line tries the clone again at once.
        .onChange(of: signIn.state) { _, s in
            if case .signedIn = s { Task { await team.signedIn() } }
        }
    }
}

/// A push that needs the owner's yes (R-0391): the workspace's first push,
/// a force or a delete. No default button.
struct PushSheet: View {
    let ask: PushAsk
    let workspace: String
    let push: PushController

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Image(systemName: "arrow.up.circle").font(.title).accessibilityHidden(true)
            Text(verbatim: PushText.title(ask, workspace: workspace)).font(.headline).accessibilityAddTraits(.isHeader)
            Text(PushText.message(ask)).fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 2) {
                ForEach(PushText.refs(ask), id: \.self) { Text(verbatim: $0).font(.system(size: 12, design: .monospaced)) }
            }
            HStack {
                Spacer()
                Button(PushText.deny) { Task { await push.answer(ask, allow: false) } }
                Button(PushText.button(ask)) { Task { await push.answer(ask, allow: true) } }
            }
        }
        .padding(20)
        .frame(width: 420)
        .interactiveDismissDisabled()
    }
}
