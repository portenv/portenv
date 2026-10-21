// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

// Pushes from the workspace (R-0391): git push in the workspace reaches
// portenvd through git-remote-portenv and work's own socket; portenvd asks
// the window's app for the owner's GitHub sign-in, which the app reads from
// the Keychain in front of the person and hands over in memory for that
// push alone. The workspace's first push, a force and a delete also need
// the owner's yes, here, with no default button.

/// One push waiting for the app.
public struct PushAsk: Equatable, Sendable, Identifiable {
    public struct Update: Equatable, Sendable {
        public var dst: String
        public var newSHA: String
        public var force: Bool
        public var delete: Bool
        public init(dst: String, newSHA: String, force: Bool, delete: Bool) {
            self.dst = dst
            self.newSHA = newSHA
            self.force = force
            self.delete = delete
        }
    }

    public var id: String
    /// github.com/OWNER/REPO.
    public var repo: String
    public var needsYes: Bool
    public var first: Bool
    /// Only the remote's refs are read (git's list for-push).
    public var list: Bool
    public var updates: [Update]
    /// Taken down: answered, or it gave up.
    public var gone = false

    public init(id: String, repo: String, needsYes: Bool, first: Bool, list: Bool, updates: [Update], gone: Bool = false) {
        self.id = id
        self.repo = repo
        self.needsYes = needsYes
        self.first = first
        self.list = list
        self.updates = updates
        self.gone = gone
    }
}

/// The window's answers to pushes.
@MainActor
@Observable
public final class PushController {
    public let box: String
    /// The push waiting for the owner's yes, if any.
    public private(set) var asking: PushAsk?
    private var queue: [PushAsk] = []
    private let daemon: DaemonAPI
    private let github: GitHubAccount
    private let retry: Duration

    public init(box: String, daemon: DaemonAPI, github: GitHubAccount = .system, retry: Duration = .seconds(2)) {
        self.box = box
        self.daemon = daemon
        self.github = github
        self.retry = retry
    }

    /// Follows the workspace's pushes while the window is open.
    public func follow() async {
        while !Task.isCancelled {
            do {
                for try await q in daemon.watchPushes(box) {
                    if q.gone {
                        queue.removeAll { $0.id == q.id }
                        if asking?.id == q.id { asking = queue.first }
                    } else if q.needsYes {
                        queue.append(q)
                        if asking == nil { asking = q }
                    } else {
                        await answer(q, allow: true)
                    }
                }
            } catch let e as DaemonError where e.message == DaemonError.outOfDate {
                return
            } catch {}
            if Task.isCancelled { return }
            try? await Task.sleep(for: retry)
        }
    }

    /// The owner's answer (or the app's own, for a routine push). Only an
    /// allowed push reads the sign-in from the Keychain.
    public func answer(_ q: PushAsk, allow: Bool) async {
        queue.removeAll { $0.id == q.id }
        if asking?.id == q.id { asking = queue.first }
        guard allow else {
            try? await daemon.answerPush(box, id: q.id, allow: false, githubToken: nil, signedOut: false)
            return
        }
        guard let token = try? await github.token(), !token.isEmpty else {
            try? await daemon.answerPush(box, id: q.id, allow: false, githubToken: nil, signedOut: true)
            return
        }
        try? await daemon.answerPush(box, id: q.id, allow: true, githubToken: token, signedOut: false)
    }
}

/// The push question's words (ADR 0025 §6, GUIDELINES §10).
public enum PushText {
    static func short(_ ref: String) -> String {
        for p in ["refs/heads/", "refs/tags/"] where ref.hasPrefix(p) { return String(ref.dropFirst(p.count)) }
        return ref
    }

    static func repoName(_ repo: String) -> String { repo.replacingOccurrences(of: "github.com/", with: "") }

    public static func title(_ q: PushAsk, workspace: String) -> String {
        let repo = repoName(q.repo)
        if let d = q.updates.first(where: \.delete) { return "Delete \(short(d.dst)) on \(repo)?" }
        if let f = q.updates.first(where: \.force) { return "Force-push to \(short(f.dst)) on \(repo)?" }
        return "Allow pushes to \(repo) from \(workspace)?"
    }

    public static func message(_ q: PushAsk) -> String {
        let only = "Pushes come only from your own tabs. Agents can never push."
        if q.updates.contains(where: \.delete) { return "It\u{2019}s removed from GitHub. " + only }
        if q.updates.contains(where: \.force) { return "This replaces what\u{2019}s there. " + only }
        return "Portenv can access your repositories; it only uses this for the repository in this workspace. " + only
    }

    public static func button(_ q: PushAsk) -> String {
        if q.updates.contains(where: \.delete) { return "Delete" }
        if q.updates.contains(where: \.force) { return "Force Push" }
        return "Allow"
    }

    public static let deny = "Don\u{2019}t Allow"

    /// Each ref the push changes, one line.
    public static func refs(_ q: PushAsk) -> [String] {
        q.updates.map { u in
            if u.delete { return "delete \(short(u.dst))" }
            return "\(short(u.dst)) \u{2192} \(u.newSHA.prefix(7))" + (u.force ? " (force)" : "")
        }
    }
}
