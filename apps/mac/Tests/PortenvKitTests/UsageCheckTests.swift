// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Usage counting on the update check (PLAN.md 1.8, R-0010, R-0126,
/// R-0369): opt-in, the published coarse fields only, no identifier, at
/// most once a day and only on days a workspace was opened, and never in
/// the way when portenv.com doesn't answer.
struct UsageCheckTests {
    /// Every name the app may send: the published list, nothing else.
    static let published: Set<String> = ["v", "os", "arch", "c", "w", "m", "moved", "agent", "boxes", "servers"]

    /// Records what would have been sent.
    final class Sent: @unchecked Sendable {
        private let lock = NSLock()
        private var _urls: [URL] = []
        var answer = true
        var urls: [URL] { lock.withLock { _urls } }
        func add(_ url: URL) { lock.withLock { _urls.append(url) } }
    }

    final class Clock: @unchecked Sendable {
        private let lock = NSLock()
        private var _now: Date
        init(_ iso: String) { _now = ISO8601DateFormatter().date(from: iso)! }
        var now: Date { lock.withLock { _now } }
        func set(_ iso: String) { lock.withLock { _now = ISO8601DateFormatter().date(from: iso)! } }
    }

    let paris = TimeZone(identifier: "Europe/Paris")!

    func defaults(optedIn: Bool) throws -> UserDefaults {
        let d = try #require(UserDefaults(suiteName: "UsageCheckTests-\(UUID().uuidString)"))
        UsageCounting.setChoice(optedIn, d)
        return d
    }

    func check(_ d: UserDefaults, _ clock: Clock, _ sent: Sent, counts: Bool = true) -> UsageCheck {
        UsageCheck(defaults: d, version: "0.2.0", macOSMajor: 26, arch: "arm64", counts: counts,
                   timeZone: paris, now: { clock.now },
                   send: { url in sent.add(url); return sent.answer })
    }

    func fields(_ url: URL) -> [String: String] {
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        return Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
    }

    let some = UsageFacts(workspaces: 3, servers: 1)

    @Test func optedOutSendsNothing() async throws {
        let d = try defaults(optedIn: false)
        let sent = Sent()
        let c = check(d, Clock("2026-10-19T09:00:00Z"), sent)
        c.recordInstallMonth()
        c.noteMoved()
        c.noteAgentWorked()
        #expect(c.workspaceOpened(some) == nil)
        #expect(sent.urls.isEmpty)
        // Opted out, nothing counting-related is kept either.
        #expect(d.object(forKey: UsageCheck.Keys.lastDay) == nil)
        #expect(!d.bool(forKey: UsageCheck.Keys.moved) && !d.bool(forKey: UsageCheck.Keys.agent))
    }

    /// Never chosen at all: the default is off (opt-in, R-0126).
    @Test func neverChosenSendsNothing() async throws {
        let d = try #require(UserDefaults(suiteName: "UsageCheckTests-\(UUID().uuidString)"))
        let sent = Sent()
        #expect(check(d, Clock("2026-10-19T09:00:00Z"), sent).workspaceOpened(some) == nil)
        #expect(sent.urls.isEmpty)
    }

    /// A dev lane's build (gmake app LANE=X) never counts, chosen or not.
    @Test func aBuildThatDoesNotCountSendsNothing() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        #expect(check(d, Clock("2026-10-19T09:00:00Z"), sent, counts: false).workspaceOpened(some) == nil)
        #expect(sent.urls.isEmpty)
    }

    @Test func theFirstCheckCarriesExactlyThePublishedFields() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        let clock = Clock("2026-10-19T09:00:00Z")
        let c = check(d, clock, sent)
        c.recordInstallMonth()
        c.noteMoved()
        c.noteAgentWorked()
        await c.workspaceOpened(some)?.value
        let url = try #require(sent.urls.first)
        #expect(sent.urls.count == 1)
        #expect(url.scheme == "https" && url.host() == "portenv.com" && url.path() == "/appcast.xml")
        #expect(fields(url) == ["v": "0.2.0", "os": "26", "arch": "arm64", "c": "2026-10", "w": "1", "m": "1",
                                "moved": "1", "agent": "1", "boxes": "2-5", "servers": "1"])
        #expect(Set(fields(url).keys).isSubset(of: Self.published))
    }

    @Test func atMostOnceADay() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        let clock = Clock("2026-10-19T09:00:00Z")
        let c = check(d, clock, sent)
        c.recordInstallMonth()
        await c.workspaceOpened(some)?.value
        #expect(c.workspaceOpened(some) == nil)
        clock.set("2026-10-19T21:30:00Z") // 23:30 in Paris: still the same day
        #expect(c.workspaceOpened(some) == nil)
        #expect(sent.urls.count == 1)
        // The next day, a plain daily check: no weekly or monthly fields.
        clock.set("2026-10-20T08:00:00Z")
        await c.workspaceOpened(some)?.value
        #expect(sent.urls.count == 2)
        #expect(fields(sent.urls[1]) == ["v": "0.2.0", "os": "26", "arch": "arm64", "c": "2026-10"])
    }

    /// The only way a check is sent is a workspace opening: days without
    /// one send nothing, and the next open sends one check, not one per day
    /// missed.
    @Test func onlyOnDaysAWorkspaceOpened() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        let clock = Clock("2026-10-19T09:00:00Z")
        let c = check(d, clock, sent)
        c.recordInstallMonth()
        clock.set("2026-10-22T09:00:00Z")
        c.noteMoved()
        #expect(sent.urls.isEmpty)
        await c.workspaceOpened(some)?.value
        #expect(sent.urls.count == 1)
    }

    @Test func weeklyAndMonthlyFlagsAndBuckets() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        let clock = Clock("2026-09-28T09:00:00Z") // Monday of ISO week 40
        let c = check(d, clock, sent)
        c.recordInstallMonth()
        await c.workspaceOpened(UsageFacts(workspaces: 1, servers: 0))?.value
        #expect(fields(sent.urls[0])["w"] == "1" && fields(sent.urls[0])["m"] == "1")
        #expect(fields(sent.urls[0])["boxes"] == "1" && fields(sent.urls[0])["servers"] == "0")
        #expect(fields(sent.urls[0])["moved"] == nil && fields(sent.urls[0])["agent"] == nil)
        // Thursday 1 October: same week, new month.
        clock.set("2026-10-01T09:00:00Z")
        c.noteMoved()
        await c.workspaceOpened(UsageFacts(workspaces: 9, servers: 7))?.value
        #expect(fields(sent.urls[1]) == ["v": "0.2.0", "os": "26", "arch": "arm64", "c": "2026-09", "m": "1"])
        // Monday of the next week: weekly fields, with the move since the
        // last weekly check.
        clock.set("2026-10-05T09:00:00Z")
        await c.workspaceOpened(UsageFacts(workspaces: 6, servers: 2))?.value
        #expect(fields(sent.urls[2]) == ["v": "0.2.0", "os": "26", "arch": "arm64", "c": "2026-09", "w": "1",
                                         "moved": "1", "boxes": "6+", "servers": "2-3"])
        // Sent and answered: the move is counted once.
        clock.set("2026-10-12T09:00:00Z")
        await c.workspaceOpened(UsageFacts(workspaces: 6, servers: 4))?.value
        #expect(fields(sent.urls[3])["moved"] == nil && fields(sent.urls[3])["servers"] == "4+")
    }

    @Test func buckets() {
        #expect([0, 1, 2, 5, 6, 40].map(UsageFacts.boxesBucket) == ["1", "1", "2-5", "2-5", "6+", "6+"])
        #expect([0, 1, 2, 3, 4, 9].map(UsageFacts.serversBucket) == ["0", "1", "2-3", "2-3", "4+", "4+"])
    }

    /// A check portenv.com never answered keeps the weekly and monthly
    /// flags for the next day's check, and still counts as the day's one
    /// check (never a retry the same day).
    @Test func anUnansweredCheckKeepsItsFlagsForTheNextDay() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        sent.answer = false
        let clock = Clock("2026-10-19T09:00:00Z")
        let c = check(d, clock, sent)
        c.recordInstallMonth()
        c.noteAgentWorked()
        await c.workspaceOpened(some)?.value
        #expect(c.workspaceOpened(some) == nil)
        sent.answer = true
        clock.set("2026-10-20T09:00:00Z")
        await c.workspaceOpened(some)?.value
        #expect(sent.urls.count == 2)
        #expect(fields(sent.urls[1])["w"] == "1" && fields(sent.urls[1])["m"] == "1" && fields(sent.urls[1])["agent"] == "1")
    }

    /// Fire and forget: opening a workspace never waits for portenv.com,
    /// even when it never answers.
    @Test func aCheckThatNeverAnswersNeverBlocks() async throws {
        let d = try defaults(optedIn: true)
        let c = UsageCheck(defaults: d, version: "0.2.0", macOSMajor: 26, arch: "arm64", counts: true,
                           timeZone: paris, now: { Date() },
                           send: { _ in try? await Task.sleep(for: .seconds(30)); return false })
        let start = ContinuousClock.now
        let task = c.workspaceOpened(some)
        #expect(task != nil)
        #expect(ContinuousClock.now - start < .milliseconds(200))
        task?.cancel()
    }

    /// The real sender: an endpoint that refuses the connection answers
    /// false and nothing throws. (Not timed here: under the one-thread
    /// pool of gmake swift-test, wall time measures the other tests.)
    @Test func theRealSenderFailsQuietlyWhenUnreachable() async throws {
        let ok = await UsageCheck.send(URL(string: "http://127.0.0.1:9/appcast.xml?v=0")!)
        #expect(!ok)
    }

    /// Its session gives up after 5 s whatever happens, never waits for
    /// the network, and keeps no cookie or cache.
    @Test func theRealSenderGivesUpQuickly() {
        let c = UsageCheck.configuration()
        #expect(c.timeoutIntervalForRequest == 5 && c.timeoutIntervalForResource == 5)
        #expect(!c.waitsForConnectivity)
        #expect(c.httpCookieStorage == nil && !c.httpShouldSetCookies && c.urlCache == nil)
    }

    /// The sender never follows the redirect to the appcast's host: the
    /// check goes to portenv.com only.
    @Test func theRealRequestCarriesNoExtraHeaders() {
        let r = UsageCheck.request(URL(string: "https://portenv.com/appcast.xml?v=0.2.0")!)
        #expect(r.value(forHTTPHeaderField: "User-Agent") == "Portenv")
        #expect(r.value(forHTTPHeaderField: "Accept-Language") == "en")
        #expect(r.httpShouldHandleCookies == false)
        #expect(r.timeoutInterval <= 5)
        #expect(r.httpMethod == "GET")
    }

    /// Turning counting off forgets the local counting state (the install
    /// month stays: it's never sent while off).
    @Test func turningItOffForgetsTheState() async throws {
        let d = try defaults(optedIn: true)
        let sent = Sent()
        let c = check(d, Clock("2026-10-19T09:00:00Z"), sent)
        c.recordInstallMonth()
        c.noteMoved()
        await c.workspaceOpened(some)?.value
        c.noteAgentWorked()
        UsageCounting.setChoice(false, d)
        for key in [UsageCheck.Keys.lastDay, UsageCheck.Keys.weekSent, UsageCheck.Keys.monthSent,
                    UsageCheck.Keys.moved, UsageCheck.Keys.agent] {
            #expect(d.object(forKey: key) == nil, "kept: \(key)")
        }
        #expect(d.string(forKey: UsageCheck.Keys.installMonth) == "2026-10")
        #expect(!UsageCounting.isOn(d))
    }

    /// The install month is the first launch's, kept across launches.
    @Test func theInstallMonthIsKept() throws {
        let d = try defaults(optedIn: false)
        let clock = Clock("2026-10-19T09:00:00Z")
        let c = check(d, clock, Sent())
        c.recordInstallMonth()
        clock.set("2027-01-04T09:00:00Z")
        c.recordInstallMonth()
        #expect(d.string(forKey: UsageCheck.Keys.installMonth) == "2026-10")
    }
}

/// What feeds `moved` and `agent`: a finished move, and an agent seen
/// running (PLAN.md 1.8).
@MainActor
struct UsageCheckSignalsTests {
    @Test func aFinishedMoveIsReported() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "demo", daemon: cli)
        var moves = 0
        c.onMoved = { moves += 1 }
        await c.open()
        #expect(moves == 0)
        cli.state = #"{"state":"SAVE_STATE_SAVED","location":"portenv@server-a"}"#
        await c.move(to: "portenv@server-a")
        #expect(moves == 1)
    }

    @Test func aRunningAgentIsReportedOnlyWhenRunning() {
        let team = TeamController(box: "demo", daemon: FakeDaemon())
        var seen = 0
        team.onAgentRunning = { seen += 1 }
        team.receive(.agent(TeamAgent(id: "lead", name: "Lead", status: .stopped, approved: true, runner: "claude")))
        #expect(seen == 0)
        team.receive(.agent(TeamAgent(id: "lead", name: "Lead", status: .running, approved: true, runner: "claude")))
        #expect(seen == 1)
    }
}
