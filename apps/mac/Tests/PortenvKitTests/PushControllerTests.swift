// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Pushes from the workspace (R-0391): portenvd asks the window's app for
/// the owner's GitHub sign-in, and for the owner's yes on the workspace's
/// first push, a force or a delete.
@MainActor
struct PushControllerTests {
    func account(signedIn: Bool) throws -> (GitHubAccount, FakeSecretStore) {
        let store = FakeSecretStore()
        if signedIn { try store.add(account: GitHubAccount.keychainAccount, data: Data("gho_abc".utf8), label: "x") }
        return (GitHubAccount(store: store, defaults: UserDefaults(suiteName: "p-\(UUID().uuidString)")!), store)
    }

    func run(_ push: PushController, until done: @escaping @MainActor () -> Bool) async {
        let task = Task { await push.follow() }
        for _ in 0 ..< 200 where !done() { try? await Task.sleep(for: .milliseconds(5)) }
        task.cancel()
        await task.value
    }

    /// A routine push: answered with the sign-in, asking nothing.
    @Test func aRoutinePushIsAnsweredWithTheSignIn() async throws {
        let daemon = FakeDaemon()
        daemon.pushQuestions = [PushAsk(id: "q1", repo: "github.com/acme/api", needsYes: false, first: false, list: true, updates: [])]
        let (github, store) = try account(signedIn: true)
        let push = PushController(box: "acme", daemon: daemon, github: github)
        await run(push) { daemon.calls.contains { $0[1] == "answer-push" } }
        #expect(daemon.calls.filter { $0[1] == "answer-push" }.map { Array($0.dropFirst(2)) } == [["acme", "q1", "allow", "token"]])
        #expect(store.ops == ["add github", "read github"] && push.asking == nil)
    }

    /// The first push, a force or a delete waits for the owner: Allow sends
    /// the sign-in; Don't Allow sends nothing and never reads the Keychain.
    @Test func aPushThatNeedsTheOwnersYesWaits() async throws {
        let daemon = FakeDaemon()
        let ask = PushAsk(id: "q2", repo: "github.com/acme/api", needsYes: true, first: false, list: false,
                          updates: [PushAsk.Update(dst: "refs/heads/main", newSHA: String(repeating: "a", count: 40), force: true, delete: false)])
        daemon.pushQuestions = [ask]
        let (github, store) = try account(signedIn: true)
        let push = PushController(box: "acme", daemon: daemon, github: github)
        await run(push) { push.asking != nil }
        #expect(push.asking == ask && !daemon.calls.contains { $0[1] == "answer-push" })
        #expect(PushText.title(ask, workspace: "acme") == "Force-push to main on acme/api?")
        #expect(PushText.message(ask) == "This replaces what\u{2019}s there. Pushes come only from your own tabs. Agents can never push.")
        #expect(PushText.button(ask) == "Force Push")
        await push.answer(ask, allow: false)
        #expect(daemon.calls.last == ["app", "answer-push", "acme", "q2", "deny", "no-token"])
        #expect(store.ops == ["add github"] && push.asking == nil)
    }

    @Test func theFirstPushAsks() {
        let first = PushAsk(id: "q", repo: "github.com/acme/api", needsYes: true, first: true, list: false,
                            updates: [PushAsk.Update(dst: "refs/heads/main", newSHA: String(repeating: "a", count: 40), force: false, delete: false)])
        #expect(PushText.title(first, workspace: "acme-api") == "Allow pushes to acme/api from acme-api?")
        #expect(PushText.button(first) == "Allow")
        let del = PushAsk(id: "q", repo: "github.com/acme/api", needsYes: true, first: false, list: false,
                          updates: [PushAsk.Update(dst: "refs/heads/old", newSHA: "", force: false, delete: true)])
        #expect(PushText.title(del, workspace: "acme-api") == "Delete old on acme/api?" && PushText.button(del) == "Delete")
    }

    /// Not signed in: portenvd hears so, and says it in the terminal.
    @Test func signedOutSaysSo() async throws {
        let daemon = FakeDaemon()
        daemon.pushQuestions = [PushAsk(id: "q3", repo: "github.com/acme/api", needsYes: false, first: false, list: true, updates: [])]
        let (github, _) = try account(signedIn: false)
        let push = PushController(box: "acme", daemon: daemon, github: github)
        await run(push) { daemon.calls.contains { $0[1] == "answer-push" } }
        #expect(daemon.calls.last == ["app", "answer-push", "acme", "q3", "signed-out", "no-token"])
    }
}

/// The inspector's "This workspace can push to …" and Stop Allowing Pushes
/// (ADR 0025 §6, CC-0645).
@MainActor
struct PushPermissionTests {
    @Test func stopAllowingPushes() async {
        let daemon = FakeDaemon()
        daemon.pushes = ("github.com/acme/api", true)
        let push = PushController(box: "acme", daemon: daemon, github: GitHubAccount(store: FakeSecretStore(), defaults: UserDefaults(suiteName: "pp-\(UUID().uuidString)")!))
        await push.refreshPermission()
        #expect(push.canPushLine == "This workspace can push to acme/api")
        await push.stopAllowing()
        #expect(daemon.calls.contains(["app", "stop-allowing-pushes", "acme"]))
        #expect(push.canPushLine == nil)
        #expect(PushText.stopAllowing == "Stop Allowing Pushes")
    }
}
