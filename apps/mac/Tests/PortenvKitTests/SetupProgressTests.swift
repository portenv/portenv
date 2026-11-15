// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// R-0409: signing in from the window's "needs a sign-in" line tries the
/// clone again at once, and the line becomes "Cloning OWNER/REPO…" while it
/// runs; then it goes, or shows the new problem.
@MainActor
struct SetupProgressTests {
    func signedOutAtFirstOpen(_ daemon: FakeDaemon, _ store: FakeSecretStore) async -> TeamController {
        daemon.appliedFirst = [TeamPlanApplied(applied: false, needsToken: true, project: "acme/acme-api"),
                               TeamPlanApplied(applied: false, needsToken: true, project: "acme/acme-api")]
        daemon.teamScripts = [.init(events: [], failure: nil)]
        let team = TeamController(box: "acme-api", daemon: daemon, retry: .milliseconds(1),
                                  github: GitHubAccount(store: store, defaults: UserDefaults(suiteName: "sp-\(UUID().uuidString)")!))
        await followBriefly(team) { team.setupProblem != nil }
        #expect(team.setupProblem?.needsSignIn == true && team.progressLine == nil)
        return team
    }

    @Test func signingInClonesAndShowsProgressThenClears() async throws {
        let daemon = FakeDaemon()
        let store = FakeSecretStore()
        let team = await signedOutAtFirstOpen(daemon, store)
        daemon.applied = TeamPlanApplied(applied: true, pinned: ["lead"])
        daemon.applyDelay = .milliseconds(300)
        try store.add(account: GitHubAccount.keychainAccount, data: Data("gho_abc".utf8), label: "x")
        let retry = Task { await team.signedIn() }
        for _ in 0 ..< 200 where !team.settingUp { try? await Task.sleep(for: .milliseconds(2)) }
        // The sign-in line is gone; the clone's progress instead.
        #expect(team.setupProblem == nil)
        #expect(team.progressLine == "Cloning acme/acme-api\u{2026}")
        await retry.value
        #expect(!team.settingUp && team.progressLine == nil && team.setupProblem == nil)
        #expect(daemon.calls.filter { $0[1] == "apply-team-plan" }.last?[3] == "token")
    }

    /// The retried clone fails: progress, then the new line.
    @Test func aFailedRetryShowsTheNewProblem() async throws {
        let daemon = FakeDaemon()
        let store = FakeSecretStore()
        let team = await signedOutAtFirstOpen(daemon, store)
        daemon.applied = TeamPlanApplied(applied: false, projectError: "acme-api needs about 14 GB with its team.", project: "acme/acme-api")
        daemon.applyDelay = .milliseconds(300)
        try store.add(account: GitHubAccount.keychainAccount, data: Data("gho_abc".utf8), label: "x")
        let retry = Task { await team.signedIn() }
        for _ in 0 ..< 200 where !team.settingUp { try? await Task.sleep(for: .milliseconds(2)) }
        #expect(team.progressLine == "Cloning acme/acme-api\u{2026}")
        await retry.value
        #expect(team.progressLine == nil && team.setupProblem?.line == "acme-api needs about 14 GB with its team.")
    }

    /// Try Again shows the same progress.
    @Test func tryAgainShowsProgress() async throws {
        let daemon = FakeDaemon()
        let store = FakeSecretStore()
        let team = await signedOutAtFirstOpen(daemon, store)
        daemon.applied = TeamPlanApplied(applied: true)
        daemon.applyDelay = .milliseconds(300)
        try store.add(account: GitHubAccount.keychainAccount, data: Data("gho_abc".utf8), label: "x")
        let retry = Task { await team.retrySetup() }
        for _ in 0 ..< 200 where !team.settingUp { try? await Task.sleep(for: .milliseconds(2)) }
        #expect(team.progressLine == "Cloning acme/acme-api\u{2026}")
        await retry.value
        #expect(team.progressLine == nil && team.setupProblem == nil)
    }

    /// Before portenvd has named the project (the first open), the line
    /// is the general one.
    @Test func theFirstOpenSaysSettingUp() async {
        let team = TeamController(box: "acme-api", daemon: FakeDaemon())
        #expect(TeamController.progressLine(settingUp: true, project: nil) == "Setting up the workspace\u{2019}s project\u{2026}")
        #expect(TeamController.progressLine(settingUp: false, project: "acme/acme-api") == nil)
        #expect(team.progressLine == nil)
    }
}
