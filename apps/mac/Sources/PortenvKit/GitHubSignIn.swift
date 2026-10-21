// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

// Sign in with GitHub (ADR 0025 §6, R-0390, R-0391): GitHub's device flow
// with Portenv's registered OAuth app, a public client with no secret. The
// token lives only in this Mac's Keychain; only the app reads it, in front
// of the person, and hands it to portenvd in memory for one clone, which
// portenvd fetches on the Mac: it never enters a workspace. A token GitHub
// has ended is never refreshed (there's no secret to do it with): the owner
// signs in again (R-0324).

/// Portenv's GitHub OAuth app: its one config place.
public enum GitHubApp {
    /// Public, and shipped in the app; there is no client secret.
    public static let clientID = "Ov23liOZgB5KvfrPxm71"
    /// Read and write on the repositories the account reaches: the least
    /// that clones (and later pushes) a private repository.
    public static let scope = "repo"
    static let deviceCodeURL = URL(string: "https://github.com/login/device/code")!
    static let tokenURL = URL(string: "https://github.com/login/oauth/access_token")!
    static let userURL = URL(string: "https://api.github.com/user")!
}

/// GitHub's endpoints: a form POST, or a GET with the token.
public protocol GitHubHTTP: Sendable {
    func send(url: URL, form: [String: String]?, bearer: String?) async throws -> (Int, Data)
}

public struct URLSessionGitHubHTTP: GitHubHTTP {
    public init() {}
    public func send(url: URL, form: [String: String]?, bearer: String?) async throws -> (Int, Data) {
        var req = URLRequest(url: url, timeoutInterval: 30)
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if let form {
            req.httpMethod = "POST"
            req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
            var c = URLComponents()
            c.queryItems = form.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
            req.httpBody = Data((c.percentEncodedQuery ?? "").utf8)
        }
        if let bearer { req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }
        let (data, resp) = try await URLSession(configuration: .ephemeral).data(for: req)
        return ((resp as? HTTPURLResponse)?.statusCode ?? 0, data)
    }
}

/// The owner's sign-in: the token in the Keychain, the login (not a
/// secret) in the defaults.
public final class GitHubAccount: @unchecked Sendable {
    public static let keychainAccount = "github"
    static let loginKey = "githubLogin"
    let store: SecretStore
    private let defaults: UserDefaults

    public init(store: SecretStore, defaults: UserDefaults = .standard) {
        self.store = store
        self.defaults = defaults
    }

    /// This Mac's, in the lane's own Keychain service.
    public static var system: GitHubAccount { GitHubAccount(store: SystemSecretStore(service: Instance.current.githubService)) }

    /// "acme-dev" once signed in; it says who, never what can be done.
    public var login: String? {
        get { defaults.string(forKey: Self.loginKey) }
        set { defaults.set(newValue, forKey: Self.loginKey) }
    }

    public var signedIn: Bool { login != nil }

    /// The token, from the Keychain (which may ask the person). Only when
    /// portenvd says a clone needs it.
    public func token() async throws -> String? {
        let store = self.store
        let data = try await onKeychainQueue { try store.read(account: Self.keychainAccount) }
        return data.map { String(decoding: $0, as: UTF8.self) }
    }

    func save(token: String) async throws {
        let store = self.store
        try await onKeychainQueue {
            try store.set(account: Self.keychainAccount, data: Data(token.utf8), label: "Portenv GitHub sign-in")
        }
    }

    func forget() async {
        let store = self.store
        try? await onKeychainQueue { try store.delete(account: Self.keychainAccount) }
        login = nil
    }
}

/// The sign-in sheet's flow.
@MainActor
@Observable
public final class GitHubSignIn: CustomStringConvertible {
    public enum State: Equatable, Sendable {
        case signedOut
        /// The code is on screen; waiting for the person to enter it.
        case waiting
        case signedIn(String)
        case failed(String)
    }

    public private(set) var state: State
    public private(set) var userCode: String?
    public private(set) var verificationURL: URL?
    public let account: GitHubAccount
    private let http: GitHubHTTP
    private let sleep: @Sendable (Duration) async -> Void
    private var deviceCode: String?
    private var interval: Int = 5
    private var expiresIn: Int = 900

    public init(http: GitHubHTTP = URLSessionGitHubHTTP(), account: GitHubAccount = .system,
                sleep: @escaping @Sendable (Duration) async -> Void = { try? await Task.sleep(for: $0) }) {
        self.http = http
        self.account = account
        self.sleep = sleep
        state = account.login.map { .signedIn($0) } ?? .signedOut
    }

    /// Never the token or the device code.
    public nonisolated var description: String { "GitHubSignIn" }

    private struct CodeAnswer: Decodable {
        let device_code: String
        let user_code: String
        let verification_uri: String
        let expires_in: Int?
        let interval: Int?
    }

    private struct TokenAnswer: Decodable {
        let access_token: String?
        let error: String?
        let interval: Int?
    }

    /// Asks GitHub for a code to show.
    public func start() async {
        do {
            let (code, data) = try await http.send(url: GitHubApp.deviceCodeURL, form: ["client_id": GitHubApp.clientID, "scope": GitHubApp.scope], bearer: nil)
            guard code == 200, let a = try? JSONDecoder().decode(CodeAnswer.self, from: data),
                  let url = URL(string: a.verification_uri), url.scheme == "https", url.host == "github.com" else {
                state = .failed(GitHubSignInText.unreachable)
                return
            }
            deviceCode = a.device_code
            userCode = TeamText.line(a.user_code, max: 20)
            verificationURL = url
            interval = max(a.interval ?? 5, 1)
            expiresIn = a.expires_in ?? 900
            state = .waiting
        } catch {
            state = .failed(GitHubSignInText.unreachable)
        }
    }

    /// Polls GitHub until the person has entered the code (or declined, or
    /// it expired), then keeps the token in the Keychain.
    public func waitForApproval() async {
        guard let deviceCode else { return }
        var waited = 0
        while state == .waiting, !Task.isCancelled, waited <= expiresIn {
            await sleep(.seconds(interval))
            waited += interval
            guard let (code, data) = try? await http.send(url: GitHubApp.tokenURL, form: [
                "client_id": GitHubApp.clientID, "device_code": deviceCode,
                "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
            ], bearer: nil), code == 200, let a = try? JSONDecoder().decode(TokenAnswer.self, from: data) else {
                state = .failed(GitHubSignInText.unreachable)
                return
            }
            if let token = a.access_token, !token.isEmpty {
                await finish(token)
                return
            }
            switch a.error {
            case "authorization_pending": continue
            case "slow_down": interval = max(a.interval ?? interval + 5, interval + 1)
            case "access_denied": state = .failed(GitHubSignInText.denied)
            case "expired_token": state = .failed(GitHubSignInText.expired)
            default: state = .failed(GitHubSignInText.unreachable)
            }
        }
        if state == .waiting { state = .failed(GitHubSignInText.expired) }
    }

    private func finish(_ token: String) async {
        do {
            try await account.save(token: token)
        } catch {
            state = .failed(GitHubSignInText.notKept)
            return
        }
        var login = "your GitHub account"
        if let (code, data) = try? await http.send(url: GitHubApp.userURL, form: nil, bearer: token), code == 200,
           let u = try? JSONDecoder().decode([String: AnyDecodable].self, from: data), let l = u["login"]?.string {
            login = TeamText.line(l, max: 39)
        }
        account.login = login
        deviceCode = nil
        userCode = nil
        state = .signedIn(login)
    }

    public func signOut() async {
        await account.forget()
        state = .signedOut
    }
}

/// A JSON value that may be a string.
struct AnyDecodable: Decodable {
    let string: String?
    init(from decoder: Decoder) throws {
        string = try? decoder.singleValueContainer().decode(String.self)
    }
}

/// The sign-in's words (GUIDELINES §10).
public enum GitHubSignInText {
    public static let button = "Sign in with GitHub\u{2026}"
    public static let title = "Sign in with GitHub"
    /// R-0391's words.
    public static let access = "Portenv asks GitHub for read and write access to your repositories; only the Portenv app uses it, agents never see it."
    public static let enterCode = "Enter this code on GitHub:"
    public static let openGitHub = "Open GitHub"
    public static let copyCode = "Copy Code"
    public static let waiting = "Waiting for you to enter the code on GitHub\u{2026}"
    public static let denied = "GitHub said no to the sign-in. Try again if you meant to allow it."
    public static let expired = "The code expired before it was entered. Try again for a new one."
    public static let unreachable = "Portenv couldn\u{2019}t reach GitHub. Check your connection, then try again."
    public static let notKept = "The Keychain didn\u{2019}t keep your GitHub sign-in. Try again."
    public static let signOut = "Sign Out of GitHub"
    public static func signedIn(_ login: String) -> String { "Signed in to GitHub as \(login)." }
}
