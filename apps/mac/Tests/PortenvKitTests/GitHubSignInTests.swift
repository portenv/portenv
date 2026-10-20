// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// GitHub's device flow, played back: each request's form, and the next
/// answer.
final class FakeGitHubHTTP: GitHubHTTP, @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [(Int, String)]
    private var _requests: [(url: String, form: [String: String], auth: String?)] = []
    var requests: [(url: String, form: [String: String], auth: String?)] { lock.withLock { _requests } }

    init(_ answers: [(Int, String)]) { self.answers = answers }

    func send(url: URL, form: [String: String]?, bearer: String?) async throws -> (Int, Data) {
        lock.withLock {
            _requests.append((url.absoluteString, form ?? [:], bearer))
            let a = answers.isEmpty ? (500, "{}") : answers.removeFirst()
            return (a.0, Data(a.1.utf8))
        }
    }
}

@MainActor
struct GitHubSignInTests {
    static let codeAnswer = (200, #"{"device_code":"dc-1","user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}"#)
    static let token = "gho_" + String(repeating: "x", count: 36)

    func flow(_ answers: [(Int, String)], store: FakeSecretStore = FakeSecretStore()) -> (GitHubSignIn, FakeGitHubHTTP, FakeSecretStore) {
        let http = FakeGitHubHTTP(answers)
        let account = GitHubAccount(store: store, defaults: UserDefaults(suiteName: "gh-\(UUID().uuidString)")!)
        return (GitHubSignIn(http: http, account: account, sleep: { _ in }), http, store)
    }

    /// The registered Portenv app, from its one place; repo scope; no secret.
    @Test func theDeviceFlowAsksForTheRepoScope() async throws {
        #expect(GitHubApp.clientID == "Ov23liOZgB5KvfrPxm71")
        let (s, http, _) = flow([Self.codeAnswer])
        await s.start()
        #expect(s.userCode == "WDJB-MJHT" && s.verificationURL?.absoluteString == "https://github.com/login/device")
        let r = try #require(http.requests.first)
        #expect(r.url == "https://github.com/login/device/code")
        #expect(r.form == ["client_id": "Ov23liOZgB5KvfrPxm71", "scope": "repo"])
        #expect(!http.requests.contains { $0.form.keys.contains("client_secret") })
    }

    /// Waits while GitHub says it's pending, then keeps the token in the
    /// Keychain and the login in the defaults; never the refresh token.
    @Test func signingInKeepsTheTokenInTheKeychain() async throws {
        let (s, http, store) = flow([
            Self.codeAnswer,
            (200, #"{"error":"authorization_pending"}"#),
            (200, #"{"error":"slow_down","interval":10}"#),
            (200, #"{"access_token":"\#(Self.token)","token_type":"bearer","scope":"repo","refresh_token":"ghr_never_kept","expires_in":28800}"#),
            (200, #"{"login":"acme-dev"}"#),
        ])
        await s.start()
        await s.waitForApproval()
        #expect(s.state == .signedIn("acme-dev"))
        #expect(store.all[GitHubAccount.keychainAccount] == Data(Self.token.utf8))
        #expect(!store.all.values.contains { String(decoding: $0, as: UTF8.self).contains("ghr_") })
        let polls = http.requests.filter { $0.url == "https://github.com/login/oauth/access_token" }
        #expect(polls.count == 3)
        #expect(polls.allSatisfy { $0.form["grant_type"] == "urn:ietf:params:oauth:grant-type:device_code" && $0.form["device_code"] == "dc-1" })
        #expect(http.requests.last?.auth == Self.token && http.requests.last?.url == "https://api.github.com/user")
        #expect(!String(describing: s).contains(Self.token))
    }

    @Test func aDeclinedOrExpiredCodeSaysSo() async {
        for (error, line) in [("access_denied", GitHubSignInText.denied), ("expired_token", GitHubSignInText.expired)] {
            let (s, _, store) = flow([Self.codeAnswer, (200, #"{"error":"\#(error)"}"#)])
            await s.start()
            await s.waitForApproval()
            #expect(s.state == .failed(line))
            #expect(store.all.isEmpty)
        }
    }

    @Test func signingOutForgetsIt() async throws {
        let store = FakeSecretStore()
        try store.add(account: GitHubAccount.keychainAccount, data: Data(Self.token.utf8), label: "x")
        let (s, _, _) = flow([], store: store)
        s.account.login = "acme-dev"
        await s.signOut()
        #expect(store.all.isEmpty && s.account.login == nil && s.state == .signedOut)
    }

    /// The sheet's words (R-0391).
    @Test func theSheetSaysWhatTheAccessIs() {
        #expect(GitHubSignInText.access.contains("read and write access to your repositories; only the Portenv app uses it, agents never see it."))
    }
}
