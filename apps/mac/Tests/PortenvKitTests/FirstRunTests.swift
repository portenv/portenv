// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Sets up keys and boxes like the app would, recording what was asked.
final class FakeFirstRunService: FirstRunService, @unchecked Sendable {
    private let lock = NSLock()
    private var _created: [NewBox] = []
    var created: [NewBox] { lock.withLock { _created } }
    /// Built at test time, so no key-shaped literal is in the source.
    static let sampleKey = Array(repeating: "TEST", count: 6).joined(separator: "-")
    var recoveryKey = FakeFirstRunService.sampleKey
    var failure: String?
    /// Fails as "not built yet" (a notice, not an error).
    var notBuilt: String?

    func makeRecoveryKey() async throws -> String {
        if let notBuilt { throw FirstRunError(notBuilt, notBuilt: true) }
        if let failure { throw FirstRunError(failure) }
        return recoveryKey
    }

    func createBox(_ box: NewBox) async throws {
        if let failure { throw FirstRunError(failure) }
        lock.withLock { _created.append(box) }
    }
}

@MainActor
struct FirstRunTests {
    /// The reduced first run (PLAN.md, "First run in Phases 1 and 2"): the
    /// same six steps, in order.
    @Test func theSixStepsInOrder() {
        #expect(FirstRunStep.allCases == [.welcome, .protection, .recoveryKey, .storage, .firstBox, .gettingReady])
        #expect(FirstRunFlow(service: FakeFirstRunService()).step == .welcome)
    }

    /// Recovery key: Continue only once the key exists and the person says
    /// they've saved it (the mockup's checkbox).
    @Test func recoveryKeyNeedsTheKeyAndTheCheckbox() async {
        let flow = FirstRunFlow(service: FakeFirstRunService())
        await flow.next() // welcome → protection
        await flow.next() // protection → recovery key (made now)
        #expect(flow.step == .recoveryKey)
        #expect(flow.recoveryKey == FakeFirstRunService.sampleKey)
        #expect(!flow.canContinue)
        flow.recoverySaved = true
        #expect(flow.canContinue)
    }

    /// No key, no recovery step: a failure keeps the person on Protection
    /// with a plain line, and Continue tries again.
    @Test func aFailedKeyStaysOnProtectionAndSaysWhy() async {
        let service = FakeFirstRunService()
        service.failure = "Portenv couldn't make your keys."
        let flow = FirstRunFlow(service: service)
        await flow.next()
        await flow.next()
        #expect(flow.step == .protection)
        #expect(flow.error == "Portenv couldn't make your keys.")
        #expect(flow.recoveryKey == nil)
        service.failure = nil
        await flow.next()
        #expect(flow.step == .recoveryKey)
        #expect(flow.error == nil)
    }

    /// Storage: one of the three choices, and a server or bucket needs its
    /// address. This Mac only says plainly that saves stay on this Mac.
    @Test func storageNeedsAChoiceAndItsAddress() {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .storage)
        #expect(!flow.canContinue)
        flow.storage = .server
        #expect(!flow.canContinue)
        flow.serverAddress = "  "
        #expect(!flow.canContinue)
        flow.serverAddress = "me@test-server"
        #expect(flow.canContinue)
        flow.storage = .bucket
        #expect(!flow.canContinue)
        flow.bucketURL = "s3:https://s3.example.com/my-bucket"
        #expect(flow.canContinue)
        flow.storage = .thisMac
        #expect(flow.canContinue)
        #expect(FirstRunText.thisMacOnly.contains("only exist on this Mac"))
    }

    /// The first box's name follows the CLI's rule (local.NameRE) and can't
    /// be a box that already exists.
    @Test func firstBoxNameFollowsTheRule() {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, existing: ["acme-api"])
        flow.boxName = ""
        #expect(!flow.canContinue)
        flow.boxName = "acme-api"
        #expect(!flow.canContinue)
        flow.boxName = "-bad"
        #expect(!flow.canContinue)
        flow.boxName = "my project"
        #expect(!flow.canContinue)
        flow.boxName = String(repeating: "a", count: 64)
        #expect(!flow.canContinue)
        flow.boxName = "site.v2_new-1"
        #expect(flow.canContinue)
    }

    /// Back goes one step back, never from Welcome or once the box is being
    /// made; leaving the recovery key never keeps the checkbox ticked.
    @Test func backGoesOneStepBack() async {
        let flow = FirstRunFlow(service: FakeFirstRunService())
        #expect(!flow.canGoBack)
        await flow.next()
        await flow.next()
        flow.recoverySaved = true
        flow.back()
        #expect(flow.step == .protection)
        #expect(!flow.recoverySaved)
        await flow.next()
        #expect(flow.step == .recoveryKey)
    }

    /// First run finished before, every box gone: only the first box and
    /// getting ready, no new keys.
    @Test func startingAtTheFirstBoxSkipsKeys() async {
        let service = FakeFirstRunService()
        let flow = FirstRunFlow(service: service, at: .firstBox)
        #expect(flow.steps == [.firstBox, .gettingReady])
        #expect(!flow.canGoBack)
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .gettingReady)
        #expect(flow.recoveryKey == nil)
        #expect(service.created == [NewBox(name: "acme-api", storage: .thisMac, serverAddress: "", bucketURL: "")])
    }

    /// The first box is made with the storage chosen, once.
    @Test func gettingReadyMakesTheBoxOnce() async {
        let service = FakeFirstRunService()
        let flow = FirstRunFlow(service: service, at: .storage)
        flow.storage = .server
        flow.serverAddress = " me@test-server "
        await flow.next()
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .gettingReady)
        #expect(service.created == [NewBox(name: "acme-api", storage: .server, serverAddress: "me@test-server", bucketURL: "")])
        #expect(flow.made == "acme-api")
        await flow.next()
        #expect(service.created.count == 1)
    }

    /// Counting (PLAN.md 1.8, and point 6 of the CNIL criteria): "Count me
    /// in" is already on and shown, with the exact fields, before any update
    /// check is sent; nothing in the list identifies anyone.
    @Test func countingIsOnAndListedInPlainWords() {
        let flow = FirstRunFlow(service: FakeFirstRunService())
        #expect(flow.countUsage)
        #expect(FirstRunText.countMeIn == "Count me in usage numbers (no ID, nothing about your work)")
        let fields = FirstRunText.countedFields.joined(separator: " ")
        for field in ["version", "macOS version", "architecture", "month you installed", "first check each week", "first check each month"] {
            #expect(fields.contains(field), "missing: \(field)")
        }
        // The IP address is seen in transit, so the line never claims it
        // isn't sent (PLAN.md 1.8, CNIL point 4).
        #expect(FirstRunText.neverCounted == "Never sent: any ID, your name or email, workspace names, file names or anything in your workspaces. Your IP address is seen in transit but never kept.")
        #expect(FirstRunText.turnOff.contains("Settings"))
    }

    /// VoiceOver reads the recovery key one character at a time, so it can
    /// be written down.
    @Test func theRecoveryKeyIsReadCharacterByCharacter() {
        #expect(A11y.recoveryKey(["AB12", "CD34"].joined(separator: "-")) == "Recovery key: A B 1 2, C D 3 4")
    }

    /// First run's words use curly apostrophes and quotes (GUIDELINES),
    /// never straight ones.
    @Test func copyUsesCurlyApostrophesAndQuotes() {
        let all = [FirstRunText.welcomeTitle, FirstRunText.welcomeLine, FirstRunText.welcomeButton,
                   FirstRunText.protectionTitle, FirstRunText.protectionLine,
                   FirstRunText.recoveryTitle, FirstRunText.recoveryLine, FirstRunText.recoverySavedCheck,
                   FirstRunText.storageTitle, FirstRunText.storageServer, FirstRunText.storageBucket,
                   FirstRunText.storageThisMac, FirstRunText.thisMacOnly, FirstRunText.firstBoxTitle,
                   FirstRunText.firstBoxLine, FirstRunText.gettingReadyTitle, FirstRunText.countMeIn,
                   FirstRunText.neverCounted, FirstRunText.turnOff, FirstRunText.whatsCounted,
                   LaunchDecision.missing("demo")] + FirstRunText.countedFields
        for line in all {
            #expect(!line.contains("'"), "straight apostrophe in: \(line)")
            #expect(!line.contains("\""), "straight quote in: \(line)")
        }
        #expect(FirstRunText.recoveryLine.hasPrefix("It\u{2019}s"))
        #expect(FirstRunText.recoverySavedCheck.hasPrefix("I\u{2019}ve"))
    }

    /// R-0032: the name field says what's wrong in one line: the board's
    /// reason when it doesn't start right, the general rule otherwise, and a
    /// name already taken; nothing while it's empty or valid.
    @Test func nameProblemsAreSaidInOneLine() {
        #expect(FirstRunText.nameProblem("", existing: []) == nil)
        #expect(FirstRunText.nameProblem("acme-api", existing: []) == nil)
        #expect(FirstRunText.nameProblem("-api", existing: []) == "Start with a letter or a number.")
        #expect(FirstRunText.nameProblem(".api", existing: []) == "Start with a letter or a number.")
        #expect(FirstRunText.nameProblem("my project", existing: []) == "Use letters, numbers and hyphens, starting with a letter or number.")
        #expect(FirstRunText.nameProblem(String(repeating: "a", count: 64), existing: []) == "Use letters, numbers and hyphens, starting with a letter or number.")
        #expect(FirstRunText.nameProblem("acme-api", existing: ["acme-api"]) == "There\u{2019}s already a workspace called \u{201C}acme-api\u{201D}.")
        #expect(FirstRunText.nameHint == "Letters, numbers and hyphens, like the project\u{2019}s name.")
        // Continue agrees with the hint.
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, existing: ["acme-api"])
        for name in ["-api", "my project", "acme-api"] {
            flow.boxName = name
            #expect(!flow.canContinue && flow.nameProblem != nil, "\(name)")
        }
        flow.boxName = "site"
        #expect(flow.canContinue && flow.nameProblem == nil)
    }

    /// From the box list it's "New box" with Create Box (the board); in
    /// first run it's "Make your first box" with Continue and the dots.
    @Test func newBoxFromTheListIsTitledNewBox() {
        let fromList = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, fromBoxList: true)
        #expect(fromList.firstBoxTitle == "New workspace")
        #expect(fromList.primaryButton == "Create Workspace")
        #expect(!fromList.showsStepDots)
        let firstRun = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox)
        #expect(firstRun.firstBoxTitle == "Make your first workspace")
        #expect(firstRun.primaryButton == "Continue")
        #expect(firstRun.showsStepDots)
    }

    /// R-0049, the new-box-linked-invalid board: while the name is invalid
    /// the field has a red border (no blue focus ring) and VoiceOver reads
    /// the problem as its description; Create Box is dimmed and disabled,
    /// not only inert. Empty is not an error, but Create Box waits.
    @Test func anInvalidNameShowsRedAndDimsCreateBox() {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .firstBox, existing: ["acme-api"], fromBoxList: true)
        flow.boxName = "-api"
        #expect(flow.nameField == NameFieldLook(showsError: true, line: "Start with a letter or a number.",
                                                accessibilityDescription: "Workspace name. Start with a letter or a number.", accessibilityHelp: nil))
        #expect(!flow.canContinue && !flow.primaryProminent)
        flow.boxName = "acme-api"
        #expect(flow.nameField.showsError)
        #expect(flow.nameField.accessibilityDescription == "Workspace name. There\u{2019}s already a workspace called \u{201C}acme-api\u{201D}.")
        #expect(!flow.primaryProminent)
        flow.boxName = ""
        #expect(flow.nameField == NameFieldLook(showsError: false, line: FirstRunText.nameHint,
                                                accessibilityDescription: "Workspace name", accessibilityHelp: FirstRunText.nameHint))
        #expect(!flow.primaryProminent)
        flow.boxName = "site"
        #expect(!flow.nameField.showsError)
        #expect(flow.canContinue && flow.primaryProminent)
    }

    /// "Not built yet" is a notice (secondary text), never an error in red,
    /// and it clears on the next try.
    @Test func notBuiltIsANoticeNotAnError() async {
        let service = FakeFirstRunService()
        service.notBuilt = "Not built yet."
        let flow = FirstRunFlow(service: service, at: .protection)
        await flow.next()
        #expect(flow.notice == "Not built yet.")
        #expect(flow.error == nil)
        #expect(flow.step == .protection)
        service.notBuilt = nil
        await flow.next()
        #expect(flow.notice == nil)
        #expect(flow.step == .recoveryKey)
    }

    /// The reduced Welcome (#16): one way in, "Continue without an account".
    @Test func welcomeIsContinueWithoutAnAccount() {
        #expect(FirstRunText.welcomeButton == "Continue without an account")
    }
}
