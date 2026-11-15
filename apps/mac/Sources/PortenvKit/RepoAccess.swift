// SPDX-License-Identifier: Apache-2.0

import Foundation

// The Project step's access check (the owner's dev-build finding 10): once
// the address is valid, ask GitHub on this Mac whether the repository can
// be cloned, before Create, rather than finding out over an empty
// workspace. GitHub's API is asked without the sign-in first, so a public
// repository is never sent it; the sign-in is read from the Keychain (by
// the app, in front of the person) only when GitHub won't show the
// repository plainly, and goes only in the Authorization header to
// api.github.com. It never enters a workspace, a URL or a log (R-0391).

/// What GitHub says about the Project step's repository.
public enum RepoAccess: Equatable, Sendable {
    /// Public: anyone can clone it.
    case publicRepo
    /// Private, and the owner's sign-in reaches it.
    case reachable
    /// Signed out, and GitHub won't show it: private, or not there.
    case needsSignIn
    /// Signed in as this login, and GitHub has nothing it can reach here.
    case notFound(String)
    /// GitHub no longer accepts Portenv's sign-in.
    case signInEnded
    /// Offline, rate-limited, or GitHub's own trouble: unknown.
    case couldNotCheck

    /// Whether Create can go on: confirmed, or unknown (the window's line
    /// after Create stays the fallback).
    public var allowsContinue: Bool {
        switch self {
        case .publicRepo, .reachable, .couldNotCheck: true
        case .needsSignIn, .notFound, .signInEnded: false
        }
    }

    /// Whether Sign in with GitHub would fix it.
    public var wantsSignIn: Bool { self == .needsSignIn || self == .signInEnded }
}

/// Asks GitHub about a repository (https://github.com/OWNER/REPO).
public protocol RepoAccessChecking: Sendable {
    func access(_ url: String) async -> RepoAccess
}

/// GitHub's repository API, through the sign-in's HTTP.
public struct GitHubRepoAccess: RepoAccessChecking {
    private let http: GitHubHTTP
    private let account: GitHubAccount

    public init(http: GitHubHTTP = URLSessionGitHubHTTP(), account: GitHubAccount = .system) {
        self.http = http
        self.account = account
    }

    public func access(_ url: String) async -> RepoAccess {
        guard let canonical = GitHubRepoURL.canonical(url),
              let api = URL(string: canonical.replacingOccurrences(of: "https://github.com/", with: "https://api.github.com/repos/")) else {
            return .couldNotCheck
        }
        guard let (plain, _) = try? await http.send(url: api, form: nil, bearer: nil) else { return .couldNotCheck }
        switch plain {
        case 200: return .publicRepo
        case 401, 404: break // private, or not there
        default: return .couldNotCheck // 403 or 429: rate-limited; 5xx: GitHub's trouble
        }
        guard let login = account.login else { return .needsSignIn }
        let token: String?
        do {
            token = try await account.token()
        } catch {
            return .couldNotCheck
        }
        guard let token, !token.isEmpty else { return .needsSignIn }
        guard let (code, _) = try? await http.send(url: api, form: nil, bearer: token) else { return .couldNotCheck }
        switch code {
        case 200: return .reachable
        case 401: return .signInEnded
        case 404: return .notFound(login)
        default: return .couldNotCheck
        }
    }
}
