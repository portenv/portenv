// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The Project step's access check, played in memory: one answer, and
/// which addresses were asked.
final class FakeRepoAccess: RepoAccessChecking, @unchecked Sendable {
    private let lock = NSLock()
    private var _answer: RepoAccess
    private var _asked: [String] = []
    /// The check takes this long (GitHub not answering).
    var delay: Duration?
    var asked: [String] { lock.withLock { _asked } }
    var answer: RepoAccess {
        get { lock.withLock { _answer } }
        set { lock.withLock { _answer = newValue } }
    }

    init(_ answer: RepoAccess) { _answer = answer }

    func access(_ url: String) async -> RepoAccess {
        lock.withLock { _asked.append(url) }
        if let delay { try? await Task.sleep(for: delay) }
        return answer
    }
}

/// A login that the test changes, as signing in does.
final class Login: @unchecked Sendable {
    private let lock = NSLock()
    private var _value: String?
    init(_ v: String?) { _value = v }
    var value: String? {
        get { lock.withLock { _value } }
        set { lock.withLock { _value = newValue } }
    }
}

/// The owner's dev-build finding 10: a private repository found out only
/// after Create. The Project step now asks GitHub, on the Mac, once the
/// address is valid; Continue waits for the answer unless it's Empty.
@MainActor
struct ProjectAccessTests {
    func atProject(_ checker: FakeRepoAccess, login: Login = Login(nil), timeout: Duration = .seconds(10)) async -> FirstRunFlow {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner(),
                                githubLogin: { login.value }, repoAccess: checker, accessDelay: .zero, accessTimeout: timeout)
        flow.boxName = "acme-api"
        await flow.next()
        flow.projectKind = .github
        flow.githubURL = "https://github.com/acme/acme-api"
        return flow
    }

    /// Before GitHub answers, Continue waits and says it's checking.
    @Test func continueWaitsForTheCheck() async {
        let flow = await atProject(FakeRepoAccess(.publicRepo))
        #expect(!flow.canContinue && flow.whyNot == ProjectText.checking)
        #expect(flow.access == nil)
    }

    @Test func aPublicRepositoryContinuesWithoutTheSignIn() async {
        let checker = FakeRepoAccess(.publicRepo)
        let flow = await atProject(checker, login: Login("acme-dev"))
        await flow.checkAccess()
        #expect(checker.asked == ["https://github.com/acme/acme-api"])
        #expect(flow.access == .publicRepo && flow.canContinue && flow.whyNot == nil)
        #expect(flow.accessLine == ProjectText.publicRepo && !flow.accessWantsSignIn)
        // A public repository is never sent the sign-in.
        #expect(flow.project == .github(url: "https://github.com/acme/acme-api", useSignIn: false))
    }

    /// Signed out and GitHub won't show it: Sign in with GitHub, right on
    /// the step; signing in checks again, and then Continue is on.
    @Test func needsSignInThenSigningInEnablesContinue() async {
        let checker = FakeRepoAccess(.needsSignIn)
        let login = Login(nil)
        let flow = await atProject(checker, login: login)
        await flow.checkAccess()
        #expect(!flow.canContinue && flow.accessWantsSignIn)
        #expect(flow.accessLine == ProjectText.privateOrMissing && flow.whyNot == ProjectText.signInToContinue)
        await flow.next()
        #expect(flow.step == .project)
        login.value = "acme-dev"
        checker.answer = .reachable
        // The answer was for the signed-out check: not used for the new sign-in.
        #expect(!flow.canContinue && flow.access == nil)
        await flow.checkAccess()
        #expect(flow.access == .reachable && flow.canContinue && !flow.accessWantsSignIn)
        #expect(flow.project == .github(url: "https://github.com/acme/acme-api", useSignIn: true))
        await flow.next()
        #expect(flow.step == .team)
    }

    @Test func notFoundWithTheSignInSaysSoPlainly() async {
        let flow = await atProject(FakeRepoAccess(.notFound("acme-dev")), login: Login("acme-dev"))
        await flow.checkAccess()
        #expect(!flow.canContinue && !flow.accessWantsSignIn)
        #expect(flow.accessLine == "GitHub has no repository at this address that acme-dev can reach. Check the address, or sign in to another account.")
        #expect(flow.whyNot == ProjectText.checkAddress)
    }

    @Test func anEndedSignInAsksToSignInAgain() async {
        let flow = await atProject(FakeRepoAccess(.signInEnded), login: Login("acme-dev"))
        await flow.checkAccess()
        #expect(!flow.canContinue && flow.accessWantsSignIn && flow.accessLine == ProjectText.signInEnded)
    }

    /// Offline or rate-limited: say so, and let them continue (the window's
    /// line stays the fallback).
    @Test func couldNotCheckLetsThemContinue() async {
        let flow = await atProject(FakeRepoAccess(.couldNotCheck))
        await flow.checkAccess()
        #expect(flow.canContinue && flow.accessLine == ProjectText.couldNotCheck)
    }

    /// GitHub not answering never blocks for ever.
    @Test func aCheckThatHangsGivesUp() async {
        let checker = FakeRepoAccess(.publicRepo)
        checker.delay = .seconds(5)
        let flow = await atProject(checker, timeout: .milliseconds(20))
        await flow.checkAccess()
        #expect(flow.access == .couldNotCheck && flow.canContinue)
    }

    /// A changed address isn't the one checked: Continue waits again.
    @Test func aChangedAddressIsCheckedAgain() async {
        let checker = FakeRepoAccess(.publicRepo)
        let flow = await atProject(checker)
        await flow.checkAccess()
        #expect(flow.canContinue)
        flow.githubURL = "https://github.com/acme/other"
        #expect(!flow.canContinue && flow.whyNot == ProjectText.checking)
        await flow.checkAccess()
        #expect(checker.asked == ["https://github.com/acme/acme-api", "https://github.com/acme/other"] && flow.canContinue)
    }

    /// Typing quickly: a check cancelled during its wait asks nothing.
    @Test func checksAreDebounced() async {
        let checker = FakeRepoAccess(.publicRepo)
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true, teamPlanner: FakeTeamPlanner(),
                                repoAccess: checker, accessDelay: .seconds(5))
        flow.boxName = "acme-api"
        await flow.next()
        flow.projectKind = .github
        flow.githubURL = "https://github.com/acme/acme-api"
        let first = Task { await flow.checkAccess() }
        first.cancel()
        await first.value
        #expect(checker.asked.isEmpty && flow.access == nil)
    }

    /// Empty never asks GitHub and always continues; an invalid address
    /// isn't sent.
    @Test func emptyAndBadAddressesAreNotChecked() async {
        let checker = FakeRepoAccess(.needsSignIn)
        let flow = await atProject(checker)
        flow.projectKind = .empty
        await flow.checkAccess()
        #expect(flow.canContinue)
        flow.projectKind = .github
        flow.githubURL = "https://gitlab.com/acme/api"
        await flow.checkAccess()
        #expect(checker.asked.isEmpty && flow.whyNot == ProjectText.notAGitHubURL)
    }
}

/// The real check: GitHub's API on the Mac, without the sign-in first; the
/// Keychain only when GitHub won't show it plainly and the owner is
/// signed in; the token only in the Authorization header.
@MainActor
struct GitHubRepoAccessTests {
    static let token = "gho_" + String(repeating: "y", count: 36)

    func checker(_ answers: [(Int, String)], signedIn: Bool) throws -> (GitHubRepoAccess, FakeGitHubHTTP, FakeSecretStore) {
        let http = FakeGitHubHTTP(answers)
        let store = FakeSecretStore()
        let account = GitHubAccount(store: store, defaults: UserDefaults(suiteName: "ra-\(UUID().uuidString)")!)
        if signedIn {
            try store.add(account: GitHubAccount.keychainAccount, data: Data(Self.token.utf8), label: "x")
            account.login = "acme-dev"
        }
        return (GitHubRepoAccess(http: http, account: account), http, store)
    }

    @Test func publicIsAskedPlainly() async throws {
        let (c, http, store) = try checker([(200, #"{"private":false}"#)], signedIn: true)
        #expect(await c.access("https://github.com/acme/acme-api") == .publicRepo)
        #expect(http.requests.map(\.url) == ["https://api.github.com/repos/acme/acme-api"])
        #expect(http.requests.allSatisfy { $0.auth == nil })
        #expect(!store.ops.contains("read github"))
    }

    @Test func signedOutNeedsSignInWithoutTheKeychain() async throws {
        let (c, http, store) = try checker([(404, "{}")], signedIn: false)
        #expect(await c.access("https://github.com/acme/acme-api") == .needsSignIn)
        #expect(http.requests.count == 1 && store.ops.isEmpty)
    }

    @Test func signedInReachesAPrivateOne() async throws {
        let (c, http, _) = try checker([(404, "{}"), (200, #"{"private":true}"#)], signedIn: true)
        #expect(await c.access("https://github.com/acme/acme-api") == .reachable)
        #expect(http.requests.map(\.auth) == [nil, Self.token])
        #expect(http.requests.allSatisfy { !$0.url.contains(Self.token) })
    }

    @Test func signedInButNotThere() async throws {
        let (c, _, _) = try checker([(404, "{}"), (404, "{}")], signedIn: true)
        #expect(await c.access("https://github.com/acme/acme-api") == .notFound("acme-dev"))
    }

    @Test func anEndedSignIn() async throws {
        let (c, _, _) = try checker([(404, "{}"), (401, "{}")], signedIn: true)
        #expect(await c.access("https://github.com/acme/acme-api") == .signInEnded)
    }

    /// Rate-limited or GitHub's own trouble: couldn't check; the sign-in
    /// isn't tried.
    @Test func rateLimitedCouldNotCheck() async throws {
        for code in [403, 429, 500] {
            let (c, http, store) = try checker([(code, "{}")], signedIn: true)
            #expect(await c.access("https://github.com/acme/acme-api") == .couldNotCheck)
            #expect(http.requests.count == 1 && !store.ops.contains("read github"))
        }
    }

    /// A Keychain that won't give the sign-in: couldn't check, never stuck.
    @Test func aRefusedKeychainCouldNotCheck() async throws {
        let (c, _, store) = try checker([(404, "{}")], signedIn: true)
        store.failReads = [GitHubAccount.keychainAccount]
        #expect(await c.access("https://github.com/acme/acme-api") == .couldNotCheck)
    }
}
