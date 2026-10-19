// SPDX-License-Identifier: Apache-2.0

import Foundation

/// What the weekly check says about this Mac, in buckets (PLAN.md 1.8).
public struct UsageFacts: Equatable, Sendable {
    public var workspaces: Int
    public var servers: Int

    public init(workspaces: Int, servers: Int) {
        self.workspaces = workspaces
        self.servers = servers
    }

    /// `boxes=1|2-5|6+`: a check is sent only on a day a workspace opened,
    /// so there's always at least one.
    public static func boxesBucket(_ n: Int) -> String {
        switch n {
        case ...1: "1"
        case 2...5: "2-5"
        default: "6+"
        }
    }

    /// `servers=0|1|2-3|4+` (connected servers).
    public static func serversBucket(_ n: Int) -> String {
        switch n {
        case ...0: "0"
        case 1: "1"
        case 2...3: "2-3"
        default: "4+"
        }
    }
}

/// Usage counting on the update check (PLAN.md 1.8, R-0010, R-0126): only
/// for a person who ticked "Count me in usage numbers", only the published
/// coarse fields, no identifier of any kind, at most one check a day and
/// only on a day a workspace was opened. Fire and forget: nothing waits for
/// it, and an unreachable portenv.com costs nothing but the lost count.
///
/// Until Sparkle (1.8) runs the update check, this is the check: a GET of
/// portenv.com/appcast.xml whose redirect is never followed. With Sparkle
/// the same query goes on Sparkle's feed request instead.
public final class UsageCheck: @unchecked Sendable {
    /// What's kept in the defaults: dates and two flags, never an ID.
    public enum Keys {
        public static let installMonth = "usage.installMonth"
        /// The day (yyyy-MM-dd, local) of the last check tried.
        public static let lastDay = "usage.lastDay"
        /// The ISO week (2026-W43) and month (2026-10) whose first check
        /// portenv.com answered.
        public static let weekSent = "usage.weekSent"
        public static let monthSent = "usage.monthSent"
        /// Since the last weekly check that was answered: a workspace moved,
        /// an agent worked.
        public static let moved = "usage.moved"
        public static let agent = "usage.agent"
        /// Forgotten when counting is turned off (the install month stays:
        /// it's never sent while off).
        static let state = [lastDay, weekSent, monthSent, moved, agent]
    }

    public static let endpoint = URL(string: "https://portenv.com/appcast.xml")!
    public static let timeout: Duration = .seconds(5)

    private let defaults: UserDefaults
    private let endpoint: URL
    private let version: String
    private let macOSMajor: Int
    private let arch: String
    private let counts: Bool
    private let calendar: Calendar
    private let now: @Sendable () -> Date
    private let send: @Sendable (URL) async -> Bool
    private let lock = NSLock()

    /// - counts: whether this build counts at all (a dev lane's never does).
    /// - send: sends the check; true once portenv.com answered.
    public init(defaults: UserDefaults = .standard, endpoint: URL = UsageCheck.endpoint,
                version: String, macOSMajor: Int, arch: String = UsageCheck.thisArch, counts: Bool,
                timeZone: TimeZone = .current, now: @escaping @Sendable () -> Date = { Date() },
                send: @escaping @Sendable (URL) async -> Bool = UsageCheck.send) {
        self.defaults = defaults
        self.endpoint = endpoint
        self.version = version
        self.macOSMajor = macOSMajor
        self.arch = arch
        self.counts = counts
        var cal = Calendar(identifier: .iso8601)
        cal.timeZone = timeZone
        calendar = cal
        self.now = now
        self.send = send
    }

    public static var thisArch: String {
        #if arch(arm64)
        "arm64"
        #else
        "x86_64"
        #endif
    }

    private var on: Bool { counts && UsageCounting.isOn(defaults) }

    /// At launch: the month Portenv was first opened here, kept locally.
    public func recordInstallMonth() {
        lock.withLock {
            if defaults.string(forKey: Keys.installMonth) == nil {
                defaults.set(monthKey(now()), forKey: Keys.installMonth)
            }
        }
    }

    /// A move finished (counted as `moved=1` on the next weekly check).
    public func noteMoved() { note(Keys.moved) }

    /// An agent ran in a workspace (`agent=1` on the next weekly check).
    public func noteAgentWorked() { note(Keys.agent) }

    private func note(_ key: String) {
        guard on else { return }
        lock.withLock { defaults.set(true, forKey: key) }
    }

    /// A workspace opened: the day's check, if counting is on and none was
    /// tried today. Returns at once; the task is for tests only.
    @discardableResult
    public func workspaceOpened(_ facts: UsageFacts) -> Task<Void, Never>? {
        guard on else { return nil }
        let date = now()
        let (week, month) = (weekKey(date), monthKey(date))
        let planned: (url: URL, weekly: Bool, monthly: Bool)? = lock.withLock {
            let today = dayKey(date)
            guard defaults.string(forKey: Keys.lastDay) != today else { return nil }
            defaults.set(today, forKey: Keys.lastDay)
            let installMonth = defaults.string(forKey: Keys.installMonth) ?? month
            let weekly = defaults.string(forKey: Keys.weekSent) != week
            let monthly = defaults.string(forKey: Keys.monthSent) != month
            var items = [URLQueryItem(name: "v", value: version),
                         URLQueryItem(name: "os", value: String(macOSMajor)),
                         URLQueryItem(name: "arch", value: arch),
                         URLQueryItem(name: "c", value: installMonth)]
            if weekly { items.append(URLQueryItem(name: "w", value: "1")) }
            if monthly { items.append(URLQueryItem(name: "m", value: "1")) }
            if weekly {
                if defaults.bool(forKey: Keys.moved) { items.append(URLQueryItem(name: "moved", value: "1")) }
                if defaults.bool(forKey: Keys.agent) { items.append(URLQueryItem(name: "agent", value: "1")) }
                items.append(URLQueryItem(name: "boxes", value: UsageFacts.boxesBucket(facts.workspaces))) // workspace-word: ok (the published wire field name)
                items.append(URLQueryItem(name: "servers", value: UsageFacts.serversBucket(facts.servers)))
            }
            var parts = URLComponents(url: endpoint, resolvingAgainstBaseURL: false)
            parts?.queryItems = items
            guard let url = parts?.url else { return nil }
            return (url, weekly, monthly)
        }
        guard let planned else { return nil }
        let send = send
        return Task.detached(priority: .utility) { [weak self] in
            guard await send(planned.url), let self else { return }
            self.lock.withLock {
                // Turned off meanwhile: nothing is kept.
                guard self.on else { return }
                if planned.weekly {
                    self.defaults.set(week, forKey: Keys.weekSent)
                    self.defaults.removeObject(forKey: Keys.moved)
                    self.defaults.removeObject(forKey: Keys.agent)
                }
                if planned.monthly { self.defaults.set(month, forKey: Keys.monthSent) }
            }
        }
    }

    private func dayKey(_ d: Date) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    private func weekKey(_ d: Date) -> String {
        let c = calendar.dateComponents([.yearForWeekOfYear, .weekOfYear], from: d)
        return String(format: "%04d-W%02d", c.yearForWeekOfYear ?? 0, c.weekOfYear ?? 0)
    }

    private func monthKey(_ d: Date) -> String {
        let c = calendar.dateComponents([.year, .month], from: d)
        return String(format: "%04d-%02d", c.year ?? 0, c.month ?? 0)
    }

    // MARK: Sending

    /// The request: a plain GET, with no cookies, no cache and no headers
    /// beyond a fixed User-Agent and language (not the Mac's).
    public static func request(_ url: URL) -> URLRequest {
        var r = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalAndRemoteCacheData,
                           timeoutInterval: Double(timeout.components.seconds))
        r.httpMethod = "GET"
        r.httpShouldHandleCookies = false
        r.setValue("Portenv", forHTTPHeaderField: "User-Agent")
        r.setValue("en", forHTTPHeaderField: "Accept-Language")
        return r
    }

    /// Sends the check and reports whether portenv.com answered. Never
    /// follows the redirect (the appcast's host never sees the check),
    /// never waits for connectivity, gives up after `timeout`.
    @Sendable
    public static func send(_ url: URL) async -> Bool {
        let session = URLSession(configuration: configuration(), delegate: NoRedirects(), delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        do {
            let (_, response) = try await session.data(for: request(url))
            return response is HTTPURLResponse
        } catch {
            return false
        }
    }

    /// Ephemeral: no cookies, no cache, nothing kept; `timeout` for the
    /// whole exchange, and never waiting for the network to come.
    public static func configuration() -> URLSessionConfiguration {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = Double(timeout.components.seconds)
        config.timeoutIntervalForResource = Double(timeout.components.seconds)
        config.waitsForConnectivity = false
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.urlCache = nil
        return config
    }

    private final class NoRedirects: NSObject, URLSessionTaskDelegate {
        func urlSession(_: URLSession, task _: URLSessionTask, willPerformHTTPRedirection _: HTTPURLResponse,
                        newRequest _: URLRequest) async -> URLRequest? { nil }
    }
}
