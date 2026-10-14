// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// First run's steps (PLAN.md, "First run in Phases 1 and 2"), reduced: no
/// sign-in, no Portenv storage, and no Protection step, whose one choice
/// ("Only you") is now a sentence on the recovery key (R-0126, R-0129).
public enum FirstRunStep: Int, CaseIterable, Sendable {
    case welcome, recoveryKey, storage, firstBox, gettingReady
}

/// Usage counting (1.8) needs the update check, which tester builds don't
/// have: its checkbox stays hidden until counting exists, and then starts
/// unticked (R-0106, R-0126). Builds with counting set PORTENV_COUNTING.
public enum UsageCounting {
    #if PORTENV_COUNTING
    public static let shown = true
    #else
    public static let shown = false
    #endif

    /// Where the person's choice is kept once counting exists (it's never
    /// on before that); Settings › Updates says what's sent from it.
    public static let choiceKey = "countUsage"

    /// Whether usage numbers are counted: only in a build with counting,
    /// and only if the person chose it.
    public static func isOn(_ defaults: UserDefaults = .standard) -> Bool {
        shown && defaults.bool(forKey: choiceKey)
    }
}

/// Where the first box's saves go (no Portenv storage before Phase 3). In
/// 1.5 only this Mac: a server and a bucket are shown dimmed, marked LATER
/// (the first-run-4-storage-this-mac board; R-0092, R-0132).
public enum StorageChoice: Equatable, Sendable {
    case server, bucket, thisMac
}

/// What first run remembers across launches: only whether a recovery key
/// was shown, never the key (ADR 0019). A quit or a crash mid-flow makes a
/// new key next time, and the recovery screen says the old one wasn't used.
public protocol FirstRunMemory: AnyObject, Sendable {
    var keyShown: Bool { get set }
}

public extension FirstRunMemory {
    /// A workspace on this Mac, however it was made (first run, or portenv
    /// init), means first run is behind us: a key shown earlier is moot.
    func forgetIfWorkspacesExist(_ workspaces: [String]) {
        if !workspaces.isEmpty { keyShown = false }
    }
}

/// The app's: a flag in the defaults.
public final class DefaultsFirstRunMemory: FirstRunMemory, @unchecked Sendable {
    public static let key = "recoveryKeyShown"
    private let defaults: UserDefaults
    public init(defaults: UserDefaults = .standard) { self.defaults = defaults }
    public var keyShown: Bool {
        get { defaults.bool(forKey: Self.key) }
        set { defaults.set(newValue, forKey: Self.key) }
    }
}

/// Remembers nothing (renders, New Workspace…).
public final class NoFirstRunMemory: FirstRunMemory, @unchecked Sendable {
    public var keyShown = false
    public init() {}
}

/// The first box, as first run makes it.
public struct NewBox: Equatable, Sendable {
    public let name: String
    public let storage: StorageChoice
    public let serverAddress: String
    public let bucketURL: String
    public init(name: String, storage: StorageChoice, serverAddress: String, bucketURL: String) {
        self.name = name
        self.storage = storage
        self.serverAddress = serverAddress
        self.bucketURL = bucketURL
    }
}

/// A failed first-run step, with its plain message (GUIDELINES §10).
public struct FirstRunError: Error, LocalizedError, Equatable {
    public let message: String
    /// A step that isn't built yet: shown as a notice (secondary text), not
    /// an error.
    public let notBuilt: Bool
    public init(_ message: String, notBuilt: Bool = false) {
        self.message = message
        self.notBuilt = notBuilt
    }
    public var errorDescription: String? { message }
}

/// What first run asks of the rest of Portenv: the keys with their recovery
/// key, and the first box. Keys and boxes are data-loss paths: the real
/// service is built with its tests and an ADR (PLAN.md 1.5).
public protocol FirstRunService: Sendable {
    /// Makes this Mac's keys and returns the recovery key, shown once.
    func makeRecoveryKey() async throws -> String
    /// The person has saved the recovery key (Continue from its step):
    /// the pool is filled from it, and it's dropped (ADR 0019).
    func recoveryKeySaved() async throws
    func createBox(_ box: NewBox) async throws
}

public extension FirstRunService {
    func recoveryKeySaved() async throws {}
}

/// The box name field's look: a red border in place of the focus ring
/// while the name is invalid, the one line under it, and what VoiceOver
/// reads: the field's description (AXDescription) names the problem, and
/// its help (AXHelp) gives the rule while the name is fine.
public struct NameFieldLook: Equatable, Sendable {
    public let showsError: Bool
    public let line: String
    public let accessibilityDescription: String
    public let accessibilityHelp: String?
    public init(showsError: Bool, line: String, accessibilityDescription: String, accessibilityHelp: String?) {
        self.showsError = showsError
        self.line = line
        self.accessibilityDescription = accessibilityDescription
        self.accessibilityHelp = accessibilityHelp
    }

    /// The field's name for VoiceOver; UI tests find it by its identifier.
    public static let fieldName = "Workspace name"
    public static let identifier = "box-name"
}

/// First run's state: which step, what's been chosen, and whether Continue
/// is enabled.
@MainActor
@Observable
public final class FirstRunFlow {
    public private(set) var step: FirstRunStep
    /// The steps this first run goes through: all five, or only the first
    /// workspace when first run was finished before (every one has since
    /// gone).
    public let steps: [FirstRunStep]
    public private(set) var recoveryKey: String?
    public var recoverySaved = false
    /// This Mac, already chosen: the only storage in 1.5.
    public var storage: StorageChoice? = .thisMac
    public var boxName = ""
    /// Usage counting: off until the person ticks it (opt-in, R-0126).
    public var countUsage = false
    public private(set) var error: String?
    /// A plain notice (secondary text), like a step not built yet.
    public private(set) var notice: String?
    /// New Box… from the box list (titled "New box", with Create Box and
    /// Cancel), rather than first run.
    public let fromBoxList: Bool
    public private(set) var busy = false
    /// The box made at the end, once it exists.
    public private(set) var made: String?
    /// How many Continues didn't move on. Each one says why (`error` or
    /// `notice`), and the view announces it again even when the words are
    /// the same, so a press never looks like nothing happened (R-0126).
    public private(set) var refusals = 0
    /// A Keychain call has waited longer than 2 s: the window says so
    /// (GUIDELINES §3.1).
    public var keychainWaiting = false
    /// The pool ran out: the window shows the recovery key sheet, and
    /// `recoveryKeyRefilled` carries on (ADR 0019, "When it runs out").
    public var needsRecoveryKey = false
    /// A recovery key was shown in an earlier launch that didn't finish
    /// first run: the one shown now replaces it (R-0132).
    public let keyShownBefore: Bool

    private let service: FirstRunService
    private let existing: Set<String>
    private let memory: FirstRunMemory

    public init(service: FirstRunService, at start: FirstRunStep = .welcome, existing: [String] = [], fromBoxList: Bool = false,
                memory: FirstRunMemory = NoFirstRunMemory()) {
        self.service = service
        self.existing = Set(existing)
        self.fromBoxList = fromBoxList
        self.memory = memory
        keyShownBefore = start == .welcome && memory.keyShown
        step = start
        steps = start == .firstBox ? [.firstBox, .gettingReady] : FirstRunStep.allCases
    }

    public var canGoBack: Bool {
        guard !busy, step != .gettingReady, let i = steps.firstIndex(of: step) else { return false }
        return i > 0
    }

    public var canContinue: Bool {
        guard !busy else { return false }
        switch step {
        case .welcome:
            return true
        case .recoveryKey:
            return recoveryKey != nil && recoverySaved
        case .storage:
            return storage == .thisMac
        case .firstBox:
            return !boxName.isEmpty && nameProblem == nil
        case .gettingReady:
            return false
        }
    }

    /// Why Continue can't go on from this step, in one line; nil while it
    /// can (or while a press is already working). It's shown above
    /// Continue while it applies, given as the button's VoiceOver hint, and
    /// announced again on every press (R-0126, R-0132).
    public var whyNot: String? {
        guard !busy, !canContinue else { return nil }
        switch step {
        case .welcome, .gettingReady:
            return nil
        case .recoveryKey:
            return recoveryKey == nil ? FirstRunText.noKeyYet : FirstRunText.tickSaved
        case .storage:
            return storage == nil ? FirstRunText.chooseStorage : FirstRunText.storageLater
        case .firstBox:
            return nameProblem ?? FirstRunText.typeName
        }
    }

    /// "Waiting for Keychain access" while a Keychain call waits, else nil.
    public var waitLine: String? { keychainWaiting ? KeychainWait.line : nil }

    /// What's said under Continue: an error, a notice, or why Continue
    /// can't go on yet.
    public var reason: String? { error ?? notice ?? whyNot }

    /// What's wrong with the name, in one line, or nil (also while empty).
    public var nameProblem: String? { FirstRunText.nameProblem(boxName, existing: Array(existing)) }

    /// How the name field looks (R-0049, the new-box-linked-invalid board):
    /// a red border while the name is invalid, and the line under it (the
    /// rule, or what's wrong) as its VoiceOver description.
    public var nameField: NameFieldLook {
        if let problem = nameProblem {
            return NameFieldLook(showsError: true, line: problem,
                                 accessibilityDescription: "\(NameFieldLook.fieldName). \(problem)", accessibilityHelp: nil)
        }
        return NameFieldLook(showsError: false, line: FirstRunText.nameHint,
                             accessibilityDescription: NameFieldLook.fieldName, accessibilityHelp: FirstRunText.nameHint)
    }

    /// Continue or Create Box is prominent (filled) only when it can be
    /// pressed; otherwise it's dimmed as well as disabled (R-0049).
    public var primaryProminent: Bool { canContinue }

    public var firstBoxTitle: String { fromBoxList ? FirstRunText.newBoxTitle : FirstRunText.firstBoxTitle }
    public var primaryButton: String { fromBoxList && step == .firstBox ? FirstRunText.createBox : "Continue" }
    public var showsStepDots: Bool { !fromBoxList }

    public func back() {
        guard canGoBack, let i = steps.firstIndex(of: step) else { return }
        if step == .recoveryKey { recoverySaved = false }
        error = nil
        notice = nil
        step = steps[i - 1]
    }

    /// Continue: the recovery key is made on leaving Welcome (Back and
    /// forward keep the same key), the box on leaving the first box. Every
    /// press either moves on or says why it can't (R-0126): a failure stays
    /// on the step with its line, and so does a step that isn't complete.
    public func next() async {
        guard let i = steps.firstIndex(of: step), i + 1 < steps.count else { return }
        guard canContinue else {
            // Busy: the press before is still working, and shows it. Else
            // the reason is already on screen; the press is counted, so it's
            // announced again.
            if whyNot != nil { refusals += 1 }
            return
        }
        busy = true
        defer { busy = false }
        do {
            switch step {
            case .welcome where recoveryKey == nil:
                recoveryKey = try await service.makeRecoveryKey()
                memory.keyShown = true
            case .recoveryKey:
                try await service.recoveryKeySaved()
            case .firstBox:
                try await service.createBox(NewBox(
                    name: boxName,
                    storage: storage ?? .thisMac,
                    serverAddress: "",
                    bucketURL: ""
                ))
                made = boxName
                memory.keyShown = false
                // First run is done: let go of the key's text (best effort,
                // R-0149).
                recoveryKey = nil
            default:
                break
            }
            error = nil
            notice = nil
            step = steps[i + 1]
        } catch let failure as FirstRunError where failure.notBuilt {
            refuse(notice: failure.message)
        } catch BoxMaker.Failure.poolEmpty {
            // The pool ran out: the recovery key sheet (ADR 0019).
            needsRecoveryKey = true
            refuse(notice: BoxMaker.Failure.poolEmpty.errorDescription)
        } catch {
            refuse(error: (error as? LocalizedError)?.errorDescription ?? error.localizedDescription)
        }
    }

    /// The sheet refilled the pool: the same Continue (Create Workspace)
    /// goes on.
    public func recoveryKeyRefilled() async {
        needsRecoveryKey = false
        notice = nil
        await next()
    }

    private func refuse(notice: String? = nil, error: String? = nil) {
        self.notice = notice
        self.error = error
        refusals += 1
    }

}

/// First run's words (GUIDELINES §10).
public enum FirstRunText {
    public static let welcomeTitle = "Welcome to Portenv"
    public static let welcomeLine = "Your portable workspace. Work on your Mac, continue on a server, and let your agents in, safely."
    /// The reduced Welcome: there's no account to be had, so just
    /// "Continue" (R-0126).
    public static let welcomeButton = "Continue"

    public static let recoveryTitle = "Save your recovery key"
    /// The Protection step's message leads, then what the key is for
    /// (R-0129's exact words).
    public static let recoveryLine = "Only you can open your workspaces\u{2019} saves: Portenv never holds your keys. This key opens them if this Mac\u{2019}s keys are ever lost, on this Mac or a new one. Keep it somewhere safe and separate from this Mac."
    public static let recoverySavedCheck = "I’ve saved my recovery key somewhere safe"
    /// A key shown in a launch that quit before first run finished was never
    /// used: this one replaces it (R-0132, ADR 0019).
    public static let replacedKey = "If you wrote down a key earlier, it wasn\u{2019}t used. Keep this one instead."
    /// While Welcome's Continue makes the key.
    public static let makingKey = "Making your recovery key\u{2026}"

    /// Why Continue can't go on yet (R-0126: never a silent press).
    public static let noKeyYet = "Your recovery key isn\u{2019}t ready yet."
    public static let tickSaved = "Tick \u{201C}I\u{2019}ve saved my recovery key somewhere safe\u{201D} to continue."
    public static let chooseStorage = "Choose where your saves should go."
    public static let storageLater = "Saving to a server or a bucket comes later. Keep your saves on this Mac for now."
    public static let typeName = "Give your workspace a name."
    public static let noBackgroundService = "Portenv\u{2019}s background service isn\u{2019}t answering. It usually starts by itself in a few seconds."
    public static let poolNotStored = "Portenv couldn\u{2019}t keep what it needs from your recovery key in the Keychain. Choose Continue to try again."

    /// The first-run-4-storage-this-mac board.
    public static let storageTitle = "Where should your saves go?"
    public static let storageLine = "Saves are encrypted wherever they\u{2019}re stored, on this Mac too."
    public static let storageThisMac = "Keep on this Mac for now"
    public static let thisMacOnly = "Saves stay on this Mac only. If this Mac is lost, they\u{2019}re lost too. You can add a server or bucket later."
    public static let storageServer = "My own server"
    public static let storageServerLine = "Over SSH, such as a Hetzner or DigitalOcean server."
    public static let storageBucket = "An S3-compatible bucket"
    public static let storageBucketLine = "AWS S3, Backblaze B2, Cloudflare R2 and others."
    public static let later = "LATER"

    public static let firstBoxTitle = "Make your first workspace"
    public static let firstBoxLine = "A workspace holds one project, encrypted. Give it a short name, like the project\u{2019}s."

    /// New Box… from the box list (the new-box board).
    public static let newBoxTitle = "New workspace"
    public static let createBox = "Create Workspace"
    public static let nameHint = "Letters, numbers and hyphens, like the project\u{2019}s name."

    /// What's wrong with a box name, in one line (R-0032), or nil when it's
    /// empty or valid. The rule is the CLI's (core/local's NameRE).
    public static func nameProblem(_ name: String, existing: [String]) -> String? {
        guard !name.isEmpty else { return nil }
        if let first = name.first, !(first.isASCII && (first.isLetter || first.isNumber)) {
            return "Start with a letter or a number."
        }
        if name.wholeMatch(of: /^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$/) == nil {
            return "Use letters, numbers and hyphens, starting with a letter or number."
        }
        if existing.contains(name) {
            return "There\u{2019}s already a workspace called \u{201C}\(name)\u{201D}."
        }
        return nil
    }

    public static let gettingReadyTitle = "Getting ready"

    /// Usage counting (1.8), shown before any update check.
    public static let countMeIn = "Count me in usage numbers (no ID, nothing about your work)"
    /// The exact fields, in plain words (PLAN.md 1.8).
    public static let countedFields = [
        "Portenv’s version, the macOS version and the Mac’s architecture",
        "the month you installed Portenv",
        "whether it’s the first check each week, and the first check each month",
        "once a week: whether a workspace moved, whether an agent worked in a workspace, and rough counts of your workspaces and servers (like 2–5)",
    ]
    public static let neverCounted = "Never sent: any ID, your name or email, workspace names, file names or anything in your workspaces. Your IP address is seen in transit but never kept."
    public static let whatsCounted = "What’s counted"
    public static let turnOff = "You can turn this off at any time in Settings › Privacy. The update check itself stays, for security updates."
}
