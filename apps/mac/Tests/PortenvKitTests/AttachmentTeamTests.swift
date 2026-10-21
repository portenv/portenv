// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Attachments in the team (ADR 0030 points 5, 8 and 9a): the chips' words,
/// what the owner always sees, the mute, and the owner's files going in.
@MainActor
@Suite(.serialized)
struct AttachmentTeamTests {
    static let shot = TeamAttachment(id: "01JAB000000000000000000001", name: "login-390.png", mediaType: "image/png", size: 1_200_000,
                                     sha256: String(repeating: "ab", count: 32), width: 390, height: 844)
    static let plan = TeamAttachment(id: "01JAB000000000000000000002", name: "redesign.md", mediaType: "text/markdown", size: 14000,
                                     sha256: String(repeating: "cd", count: 32), inboxPath: "inbox/builder/20261019T142800Z/redesign.md")
    static let spec = TeamAttachment(id: "01JAB000000000000000000003", name: "Spec.docx",
                                     mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", size: 30000,
                                     sha256: String(repeating: "ef", count: 32))

    func defaults() -> UserDefaults {
        let name = "attach-tests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    func team(_ daemon: FakeDaemon = FakeDaemon(), defaults: UserDefaults? = nil, started: Date = Date(timeIntervalSince1970: 0),
              notices: NoticeLog = NoticeLog()) -> TeamController {
        let t = TeamController(box: "acme", daemon: daemon, defaults: defaults ?? self.defaults(), startedAt: started) { notices.all.append($0) }
        t.receive(.agent(TeamAgent(id: "builder", name: "Builder", status: .running, approved: true)))
        t.receive(.agent(TeamAgent(id: "reviewer", name: "Reviewer", status: .idle, approved: true)))
        return t
    }

    final class NoticeLog { var all: [AttachmentNotice] = [] }

    // MARK: Words

    @Test func chipsSayNameTypeAndSize() {
        #expect(AttachmentText.chip(Self.plan) == "redesign.md \u{00B7} Markdown \u{00B7} 14 KB")
        #expect(AttachmentText.chip(Self.shot) == "login-390.png \u{00B7} PNG image \u{00B7} 1.2 MB")
        #expect(AttachmentText.chip(Self.spec) == "Spec.docx \u{00B7} Word document \u{00B7} 30 KB")
        #expect(AttachmentText.chip(Self.plan, availability: .elsewhere) == "redesign.md \u{00B7} on another Mac")
        #expect(AttachmentText.chip(Self.plan, availability: .removed) == "redesign.md \u{00B7} removed")
        #expect(AttachmentText.symbol(Self.plan) == "doc.richtext" && AttachmentText.symbol(TeamAttachment(id: "x", name: "a.txt", mediaType: "text/plain"))
            == "doc.text" && AttachmentText.symbol(Self.spec) == "doc")
        #expect(Self.spec.kind.previewable == false && Self.plan.kind.previewable && Self.shot.kind.isImage)
        // An agent's name is plain text; control characters never reach a chip.
        let hostile = TeamAttachment(id: "x", name: "evil\u{202E}gnp.exe\u{1b}[2J", mediaType: "image/png", size: 3)
        #expect(!AttachmentText.chip(hostile).contains("\u{202E}") && !AttachmentText.chip(hostile).contains("\u{1b}"))
    }

    @Test func voiceOverReadsEachChipAsOneThing() {
        #expect(AttachmentText.spoken(Self.shot, from: "Builder")
            == "Attachment: login-390.png, PNG image, 1.2 MB, from Builder. Opens a preview.")
        #expect(AttachmentText.spoken(Self.plan, from: "Builder", availability: .elsewhere)
            == "Attachment: redesign.md, Markdown, 14 KB, from Builder, on another Mac, can\u{2019}t be shown.")
        #expect(AttachmentText.spoken(Self.spec, from: "You")
            == "Attachment: Spec.docx, Word document, 30 KB, from You. Save it to your Mac to open it.")
        #expect(AttachmentText.saveLabel(Self.shot) == "Save login-390.png to your Mac")
        let at = Date(timeIntervalSince1970: 1_792_400_000)
        #expect(AttachmentText.header(Self.plan, from: "Builder", at: at, timeZone: TimeZone(identifier: "UTC")!)
            == "redesign.md \u{00B7} from Builder \u{00B7} 08:53 \u{00B7} 14 KB")
        #expect(AttachmentText.pageLabel(3, of: 12) == "Page 3 of 12")
    }

    // MARK: What the owner always sees (R-0381)

    @Test func anAgentToAgentAttachmentIsOnItsMessageAndInThePanel() {
        let t = team()
        t.receive(.message(TeamMessage(id: "m1", from: "builder", to: "reviewer", text: "Review this", createdAt: Date(timeIntervalSince1970: 10),
                                       attachments: [Self.plan])))
        t.receive(.decision(TeamDecision(id: "d1", from: "builder", kind: .askOwner, summary: "Ship?", detail: "Ship?", state: .pending,
                                         createdAt: Date(timeIntervalSince1970: 20), attachments: [Self.shot])))
        #expect(t.state.activity().first?.attachments == [Self.plan])
        let all = t.state.attachmentEntries()
        #expect(all.map(\.attachment) == [Self.shot, Self.plan], "newest first")
        #expect(all[1].route(team: t.state.agents) == "Builder \u{2192} Reviewer")
        #expect(all[0].route(team: t.state.agents) == "Builder \u{2192} You")
        #expect(t.state.attachmentEntries(filter: "reviewer").map(\.attachment) == [Self.plan])
        #expect(t.state.attachmentEntries(filter: "builder").count == 2)
        #expect(AttachmentText.panelHeading(all.count) == "Attachments \u{00B7} 2")
    }

    /// Muting an agent's attachments silences their notifications only:
    /// the chips, the panel and the message's own notice stay.
    @Test func mutingStopsNotificationsAndHidesNothing() {
        let log = NoticeLog()
        let defaults = defaults()
        let t = team(defaults: defaults, started: Date(timeIntervalSince1970: 100), notices: log)
        let m = TeamMessage(id: "m2", from: "builder", to: "owner", text: "Here it is", createdAt: Date(timeIntervalSince1970: 200), attachments: [Self.shot])
        t.receiveLive(.message(m))
        #expect(log.all == [AttachmentNotice(agent: "builder", title: "Builder attached login-390.png", body: "Here it is")])
        t.setMuted(true, agent: "builder")
        #expect(t.isMuted("builder") && TeamController.mutedLine == "Attachments muted")
        #expect(AttachmentText.mute("Builder", muted: false) == "Mute Attachments from Builder"
            && AttachmentText.mute("Builder", muted: true) == "Unmute Attachments from Builder")
        t.receiveLive(.message(TeamMessage(id: "m3", from: "builder", to: "owner", text: "Another", createdAt: Date(timeIntervalSince1970: 300), attachments: [Self.plan])))
        t.receiveLive(.decision(TeamDecision(id: "d2", from: "builder", kind: .askOwner, summary: "Ship?", detail: "", state: .pending,
                                             createdAt: Date(timeIntervalSince1970: 300), attachments: [Self.plan])))
        #expect(log.all.count == 1, "muted, it still notified")
        #expect(t.state.attachmentEntries().count == 3 && t.state.conversation(with: "builder").last?.attachments == [Self.plan])
        #expect(t.state.needsYou.first?.attachments == [Self.plan])
        // Remembered per workspace on this Mac.
        let again = TeamController(box: "acme", daemon: FakeDaemon(), defaults: defaults)
        #expect(again.isMuted("builder"))
        #expect(!TeamController(box: "other", daemon: FakeDaemon(), defaults: defaults).isMuted("builder"))
        // Another agent, and the board read back at the start, raise nothing extra.
        t.receiveLive(.message(TeamMessage(id: "m4", from: "reviewer", to: "owner", text: "Report", createdAt: Date(timeIntervalSince1970: 400), attachments: [Self.plan])))
        t.receiveLive(.message(TeamMessage(id: "m0", from: "reviewer", to: "owner", text: "Old", createdAt: Date(timeIntervalSince1970: 50), attachments: [Self.plan])))
        t.receiveLive(.message(TeamMessage(id: "m5", from: "reviewer", to: "builder", text: "Not yours", createdAt: Date(timeIntervalSince1970: 400), attachments: [Self.plan])))
        #expect(log.all.map(\.agent) == ["builder", "reviewer"])
    }

    // MARK: Here, removed, on another Mac

    @Test func availabilityFromTheStore() async throws {
        let daemon = FakeDaemon()
        daemon.attachmentStore[Self.shot.id] = (Self.shot, Data("png".utf8))
        let defaults = defaults()
        let t = team(daemon, defaults: defaults)
        #expect(t.availability(of: Self.shot) == .unknown)
        await t.check(Self.shot)
        await t.check(Self.plan)
        #expect(t.availability(of: Self.shot) == .here && t.availability(of: Self.plan) == .elsewhere)
        #expect(try await t.data(of: Self.shot) == Data("png".utf8))
        await t.remove(Self.shot)
        #expect(t.availability(of: Self.shot) == .removed)
        #expect(daemon.calls.contains(["app", "remove-attachment", "acme", Self.shot.id]))
        // A new window on this Mac still says removed, not "on another Mac".
        let again = TeamController(box: "acme", daemon: daemon, defaults: defaults)
        await again.check(Self.shot)
        #expect(again.availability(of: Self.shot) == .removed)
    }

    // MARK: The owner's files: checked before anything is sent

    func files(_ named: [String: Data]) throws -> (URL, [URL]) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("attach-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        var urls: [URL] = []
        for (n, d) in named.sorted(by: { $0.key < $1.key }) {
            let u = dir.appendingPathComponent(n)
            try d.write(to: u)
            urls.append(u)
        }
        return (dir, urls)
    }

    static let pngBytes = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13])

    @Test func foldersLinksUnknownTypesAndTooManyAreRefusedBeforeSending() async throws {
        let (dir, urls) = try files(["SPEC.md": Data("# Spec".utf8), "shot.png": Self.pngBytes, "logo.svg": Data("<svg/>".utf8),
                                     "tool": Data([0x7F, 0x45, 0x4C, 0x46]), "notes.pdf": Data("not a pdf".utf8)])
        defer { try? FileManager.default.removeItem(at: dir) }
        let folder = dir.appendingPathComponent("drafts", isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let link = dir.appendingPathComponent("link.md")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: urls.first { $0.lastPathComponent == "SPEC.md" }!)
        let byName = Dictionary(uniqueKeysWithValues: urls.map { ($0.lastPathComponent, $0) })
        for (picked, line) in [
            ([folder], "Folders can\u{2019}t be attached yet. Drop files."),
            ([link], "link.md is a link. Attach the file itself."),
            ([byName["logo.svg"]!], "logo.svg isn\u{2019}t a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."),
            ([byName["tool"]!], "tool isn\u{2019}t a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."),
            ([byName["notes.pdf"]!], "notes.pdf isn\u{2019}t a PDF, PNG, JPEG, GIF, WebP, Markdown, text or Word file. Attach one of those."),
            (Array(repeating: byName["SPEC.md"]!, count: 6), "Attach at most 5 files at once."),
            ([byName["SPEC.md"]!, folder], "Folders can\u{2019}t be attached yet. Drop files."),
        ] {
            let daemon = FakeDaemon()
            let t = team(daemon)
            await t.attach(picked, to: ["builder"], target: "builder")
            #expect(t.attachRefusals["builder"] == line)
            #expect(daemon.sentFiles.isEmpty, "\(picked.map(\.lastPathComponent)) reached portenvd")
        }
    }

    /// Only the cleaned base name and the bytes go to portenvd: the Mac path
    /// never leaves the app.
    @Test func theMacPathNeverLeavesTheApp() async throws {
        let (dir, urls) = try files(["SPEC.md": Data("# Spec".utf8), "shot.png": Self.pngBytes])
        defer { try? FileManager.default.removeItem(at: dir) }
        let daemon = FakeDaemon()
        daemon.sendResult = SentAttachments(
            messages: [TeamMessage(id: "m9", from: "owner", to: "builder", text: "attached 2 files", attachments: [Self.plan, Self.shot])],
            refused: [], inserted: .none, insertedPath: "/home/builder/inbox/owner/20261019T142800Z/")
        let t = team(daemon)
        await t.attach(urls, to: ["builder"], pane: "%3", target: "tab:builder:@1")
        let sent = try #require(daemon.sentFiles.first)
        #expect(sent.to == ["builder"] && sent.pane == "%3")
        #expect(sent.files.map(\.name) == ["SPEC.md", "shot.png"])
        let record = daemon.calls.flatMap { $0 }.joined(separator: " ")
        #expect(!record.contains(dir.path) && !record.contains(NSHomeDirectory()) && !sent.files.contains { $0.name.contains("/") })
        #expect(t.state.messages.contains { $0.id == "m9" })
        // Nothing typed: the path is shown, to copy.
        #expect(t.dropNotice == DropNotice(line: "2 files are in Builder\u{2019}s inbox: ~/inbox/owner/20261019T142800Z/",
                                           path: "/home/builder/inbox/owner/20261019T142800Z/"))
        #expect(AttachmentText.copyPath == "Copy Path")
    }

    @Test func aDropReportsHowThePathWasOfferedAndEachRefusal() async throws {
        let (dir, urls) = try files(["SPEC.md": Data("# Spec".utf8)])
        defer { try? FileManager.default.removeItem(at: dir) }
        let daemon = FakeDaemon()
        let specMD = TeamAttachment(id: "01JAB000000000000000000004", name: "SPEC.md", mediaType: "text/markdown", size: 6,
                                    inboxPath: "inbox/owner/20261019T142800Z/SPEC.md")
        daemon.sendResult = SentAttachments(messages: [TeamMessage(id: "m1", from: "owner", to: "builder", text: "attached SPEC.md", attachments: [specMD])],
                                            refused: ["Reviewer\u{2019}s files are full (2.0 GB of 2.0 GB), so it didn\u{2019}t get SPEC.md."],
                                            inserted: .typed, insertedPath: "/home/builder/inbox/owner/20261019T142800Z/SPEC.md")
        let t = team(daemon)
        await t.attach(urls, to: ["builder", "reviewer"], target: "activity")
        #expect(t.dropNotice == nil, "typed at the prompt: no line")
        #expect(t.attachRefusals["activity"] == "Reviewer\u{2019}s files are full (2.0 GB of 2.0 GB), so it didn\u{2019}t get SPEC.md.")
        daemon.sendResult.inserted = .none
        await t.attach(urls, to: ["builder"], target: "builder")
        #expect(t.dropNotice?.line == "SPEC.md is in Builder\u{2019}s inbox: ~/inbox/owner/20261019T142800Z/SPEC.md")
        // A pasted image goes the same way, as a PNG.
        await t.attach([OutgoingAttachments.pasted(png: Self.pngBytes)], to: ["builder"], target: "builder")
        #expect(daemon.sentFiles.last?.files.map(\.name) == ["Pasted image.png"])
    }
}
