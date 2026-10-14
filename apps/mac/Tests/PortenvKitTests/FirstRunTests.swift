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
    /// A different key on every call (to catch a key made twice).
    var newKeyEachCall = false
    private var _keysMade = 0
    var keysMade: Int { lock.withLock { _keysMade } }
    var failure: String?
    /// The first this many createBox calls find portenvd down (R-0202).
    var serviceDownFor = 0
    private var _creates = 0
    var creates: Int { lock.withLock { _creates } }
    /// Runs before each createBox (to look at the flow mid-call).
    var onCreate: (@Sendable () async -> Void)?
    /// Fails as "not built yet" (a notice, not an error).
    var notBuilt: String?

    func makeRecoveryKey() async throws -> String {
        if let notBuilt { throw FirstRunError(notBuilt, notBuilt: true) }
        if let failure { throw FirstRunError(failure) }
        let n = lock.withLock { _keysMade += 1; return _keysMade }
        return newKeyEachCall ? Array(repeating: "K\(n)AB", count: 6).joined(separator: "-") : recoveryKey
    }

    func createBox(_ box: NewBox) async throws {
        if let failure { throw FirstRunError(failure) }
        await onCreate?()
        let n = lock.withLock { _creates += 1; return _creates }
        if n <= serviceDownFor { throw DaemonError(DaemonErrorText.starting, unavailable: true) }
        lock.withLock { _created.append(box) }
    }
}

@MainActor
struct FirstRunTests {
    /// The reduced first run: five steps, the Protection step gone into the
    /// recovery key's line (R-0126, R-0129).
    @Test func theFiveStepsInOrder() {
        #expect(FirstRunStep.allCases == [.welcome, .recoveryKey, .storage, .firstBox, .gettingReady])
        #expect(FirstRunFlow(service: FakeFirstRunService()).step == .welcome)
    }

    /// Recovery key: Continue only once the key exists and the person says
    /// they've saved it (the mockup's checkbox).
    @Test func recoveryKeyNeedsTheKeyAndTheCheckbox() async {
        let flow = FirstRunFlow(service: FakeFirstRunService())
        await flow.next() // welcome → recovery key (made now)
        #expect(flow.step == .recoveryKey)
        #expect(flow.recoveryKey == FakeFirstRunService.sampleKey)
        #expect(!flow.canContinue)
        flow.recoverySaved = true
        #expect(flow.canContinue)
    }

    /// No key, no recovery step: a failure keeps the person on Welcome with
    /// a plain line, and Continue tries again.
    @Test func aFailedKeyStaysOnWelcomeAndSaysWhy() async {
        let service = FakeFirstRunService()
        service.failure = "Portenv couldn't make your keys."
        let flow = FirstRunFlow(service: service)
        await flow.next()
        #expect(flow.step == .welcome)
        #expect(flow.error == "Portenv couldn't make your keys.")
        #expect(flow.recoveryKey == nil)
        service.failure = nil
        await flow.next()
        #expect(flow.step == .recoveryKey)
        #expect(flow.error == nil)
    }

    /// Storage (the first-run-4-storage-this-mac board, R-0132): this Mac
    /// is already chosen, and it's the only choice; a server or a bucket
    /// comes later and can't go on.
    @Test func storageIsThisMacForNow() {
        let flow = FirstRunFlow(service: FakeFirstRunService(), at: .storage)
        #expect(flow.storage == .thisMac)
        #expect(flow.canContinue && flow.whyNot == nil)
        flow.storage = .server
        #expect(!flow.canContinue)
        #expect(flow.whyNot == FirstRunText.storageLater)
        flow.storage = nil
        #expect(flow.whyNot == FirstRunText.chooseStorage)
        #expect(FirstRunText.storageThisMac == "Keep on this Mac for now")
        #expect(FirstRunText.thisMacOnly == "Saves stay on this Mac only. If this Mac is lost, they\u{2019}re lost too. You can add a server or bucket later.")
        #expect(FirstRunText.later == "LATER")
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
        let service = FakeFirstRunService()
        service.newKeyEachCall = true
        let flow = FirstRunFlow(service: service)
        #expect(!flow.canGoBack)
        await flow.next()
        let key = flow.recoveryKey
        flow.recoverySaved = true
        flow.back()
        #expect(flow.step == .welcome)
        #expect(!flow.recoverySaved)
        await flow.next()
        #expect(flow.step == .recoveryKey)
        #expect(flow.recoveryKey == key) // Back and forward keep the same key
        #expect(service.keysMade == 1)
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
        await flow.next()
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .gettingReady)
        #expect(service.created == [NewBox(name: "acme-api", storage: .thisMac, serverAddress: "", bucketURL: "")])
        #expect(flow.made == "acme-api")
        await flow.next()
        #expect(service.created.count == 1)
    }

    /// Counting (PLAN.md 1.8, and point 6 of the CNIL criteria): hidden
    /// while nothing is counted, and unticked when it comes (opt-in,
    /// R-0106, R-0126), with the exact fields; nothing in the list
    /// identifies anyone.
    @Test func countingIsHiddenUntickedAndListedInPlainWords() {
        let flow = FirstRunFlow(service: FakeFirstRunService())
        #expect(!UsageCounting.shown)
        #expect(!flow.countUsage)
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
                   FirstRunText.recoveryTitle, FirstRunText.recoveryLine, FirstRunText.recoverySavedCheck,
                   FirstRunText.noKeyYet, FirstRunText.tickSaved, FirstRunText.chooseStorage, FirstRunText.storageLater,
                   FirstRunText.typeName, FirstRunText.replacedKey, FirstRunText.makingKey, FirstRunText.storageLine,
                   FirstRunText.storageServerLine, FirstRunText.storageBucketLine,
                   FirstRunText.storageTitle, FirstRunText.storageServer, FirstRunText.storageBucket,
                   FirstRunText.storageThisMac, FirstRunText.thisMacOnly, FirstRunText.firstBoxTitle,
                   FirstRunText.firstBoxLine, FirstRunText.gettingReadyTitle, FirstRunText.countMeIn,
                   FirstRunText.neverCounted, FirstRunText.turnOff, FirstRunText.whatsCounted,
                   LaunchDecision.missing("demo")] + FirstRunText.countedFields
        for line in all {
            #expect(!line.contains("'"), "straight apostrophe in: \(line)")
            #expect(!line.contains("\""), "straight quote in: \(line)")
        }
        #expect(FirstRunText.recoveryLine.contains("workspaces\u{2019} saves"))
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
        let flow = FirstRunFlow(service: service)
        await flow.next()
        #expect(flow.notice == "Not built yet.")
        #expect(flow.error == nil)
        #expect(flow.step == .welcome)
        service.notBuilt = nil
        await flow.next()
        #expect(flow.notice == nil)
        #expect(flow.step == .recoveryKey)
    }

    /// Quitting mid-flow and opening again makes a new key, and says the
    /// old one wasn't used (R-0132, ADR 0019). Only "a key was shown" is
    /// remembered, never the key; finishing first run forgets it.
    @Test func aKeyFromAnEarlierLaunchIsSaidToBeReplaced() async {
        final class Memory: FirstRunMemory, @unchecked Sendable { var keyShown = false }
        let memory = Memory()
        let first = FirstRunFlow(service: FakeFirstRunService(), memory: memory)
        #expect(!first.keyShownBefore)
        await first.next()
        #expect(memory.keyShown)
        // Quit here, open again.
        let service = FakeFirstRunService()
        service.newKeyEachCall = true
        let second = FirstRunFlow(service: service, memory: memory)
        #expect(second.keyShownBefore)
        await second.next()
        #expect(second.recoveryKey != first.recoveryKey)
        #expect(FirstRunText.replacedKey == "If you wrote down a key earlier, it wasn\u{2019}t used. Keep this one instead.")
        second.recoverySaved = true
        await second.next() // storage
        await second.next() // first workspace
        second.boxName = "acme-api"
        await second.next()
        #expect(second.made == "acme-api")
        #expect(!memory.keyShown)
        #expect(!FirstRunFlow(service: service, memory: memory).keyShownBefore)
    }

    /// Any workspace on this Mac, made by first run or by portenv init,
    /// forgets that a key was shown: the note would be wrong (R-0133).
    @Test func anyWorkspaceForgetsTheShownKey() {
        let memory = DefaultsFirstRunMemory(defaults: UserDefaults(suiteName: "portenv-test-\(UUID().uuidString)")!)
        memory.keyShown = true
        memory.forgetIfWorkspacesExist([])
        #expect(memory.keyShown)
        memory.forgetIfWorkspacesExist(["made-with-portenv-init"])
        #expect(!memory.keyShown)
    }

    /// Welcome's one way in is "Continue": there's no account to be had
    /// (R-0126).
    @Test func welcomeIsJustContinue() {
        #expect(FirstRunText.welcomeButton == "Continue")
    }

    /// The recovery key's line starts with what Protection used to say
    /// (R-0129's exact words).
    @Test func theRecoveryKeyLineCarriesProtection() {
        #expect(FirstRunText.recoveryLine == "Only you can open your workspaces\u{2019} saves: Portenv never holds your keys. This key opens them if this Mac\u{2019}s keys are ever lost, on this Mac or a new one. Keep it somewhere safe and separate from this Mac.")
    }

    /// R-0126: walk forwards and backwards through every step, then all the
    /// way forwards again. Every press of Continue either moves on or says
    /// why it can't (a refusal is counted, so the view announces it), and
    /// Back always lands on the step before.
    @Test func everyContinueMovesOnOrSaysWhy() async {
        let service = FakeFirstRunService()
        let flow = FirstRunFlow(service: service)
        let steps = FirstRunStep.allCases

        /// Presses Continue; it must move on or say why.
        func press(expect moved: Bool) async {
            let before = flow.step, refusals = flow.refusals
            await flow.next()
            if flow.step != before {
                #expect(moved, "moved on from \(before) without being ready")
                #expect(flow.step == steps[steps.firstIndex(of: before)! + 1])
                #expect(flow.error == nil && flow.notice == nil)
                #expect(flow.reason == flow.whyNot)
            } else {
                #expect(!moved, "stayed on \(before)")
                #expect(flow.refusals == refusals + 1, "a silent press on \(before)")
                #expect(flow.reason != nil, "no reason on \(before)")
            }
        }
        /// Makes the step complete, after first pressing while it isn't.
        func complete() async {
            switch flow.step {
            case .recoveryKey:
                await press(expect: false) // not ticked yet
                #expect(flow.reason == FirstRunText.tickSaved)
                flow.recoverySaved = true
                #expect(flow.reason == nil) // gone once it no longer applies
            case .storage:
                #expect(flow.storage == .thisMac) // already chosen
            case .firstBox:
                flow.boxName = ""
                await press(expect: false)
                #expect(flow.reason == FirstRunText.typeName)
                flow.boxName = "-x"
                await press(expect: false)
                #expect(flow.reason == "Start with a letter or a number.")
                flow.boxName = "acme-api"
            default:
                break
            }
        }

        // Forwards to the first workspace, Back one step at a time to
        // Welcome, then all the way forwards.
        for _ in 0..<3 {
            await complete()
            await press(expect: true)
        }
        #expect(flow.step == .firstBox)
        for target in [FirstRunStep.storage, .recoveryKey, .welcome] {
            #expect(flow.canGoBack)
            flow.back()
            #expect(flow.step == target)
        }
        #expect(!flow.canGoBack)
        while flow.step != .gettingReady {
            await complete()
            await press(expect: true)
        }
        #expect(service.created.map(\.name) == ["acme-api"])

        // A step that can't be built yet (0.1.0-68's Continue on step 2,
        // R-0126) says so on every press, not only the first.
        let stuck = FakeFirstRunService()
        stuck.notBuilt = "Not ready yet."
        let flow2 = FirstRunFlow(service: stuck)
        for n in 1...3 {
            await flow2.next()
            #expect(flow2.step == .welcome)
            #expect(flow2.notice == "Not ready yet.")
            #expect(flow2.refusals == n)
        }
    }

    /// portenvd isn't up yet (or restarts): first run says it's starting it
    /// and tries again, rather than failing on the first refused
    /// connection; never a socket path (R-0202).
    @Test func firstRunWaitsForTheServiceToComeUp() async {
        let service = FakeFirstRunService()
        service.serviceDownFor = 3
        let flow = FirstRunFlow(service: service, at: .firstBox, retryEvery: .milliseconds(5), retryFor: .seconds(600)) // room: on CI, other suites hold the main actor for a minute
        flow.boxName = "acme-api"
        final class Said: @unchecked Sendable { var lines: Set<String> = [] }
        let said = Said()
        service.onCreate = { @MainActor in if let n = flow.notice { said.lines.insert(n) } }
        await flow.next()
        #expect(flow.step == .gettingReady)
        #expect(service.creates == 4)
        #expect(flow.error == nil)
        #expect(said.lines.contains(DaemonErrorText.starting))
    }

    /// One that never comes up: the plain line, as an error, once the wait
    /// is over.
    @Test func aServiceThatNeverComesUpSaysSoPlainly() async {
        let service = FakeFirstRunService()
        service.serviceDownFor = 1_000
        let flow = FirstRunFlow(service: service, at: .firstBox, retryEvery: .milliseconds(5), retryFor: .milliseconds(100))
        flow.boxName = "acme-api"
        await flow.next()
        #expect(flow.step == .firstBox)
        #expect(flow.error == DaemonErrorText.starting)
    }
}
