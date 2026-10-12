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
}

/// Where the first box's saves go (no Portenv storage before Phase 3).
public enum StorageChoice: Equatable, Sendable {
    case server, bucket, thisMac
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
    func createBox(_ box: NewBox) async throws
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
    public static let fieldName = "Box name"
    public static let identifier = "box-name"
}

/// First run's state: which step, what's been chosen, and whether Continue
/// is enabled.
@MainActor
@Observable
public final class FirstRunFlow {
    public private(set) var step: FirstRunStep
    /// The steps this first run goes through: all six, or only the first box
    /// when first run was finished before (every box has since gone).
    public let steps: [FirstRunStep]
    public private(set) var recoveryKey: String?
    public var recoverySaved = false
    public var storage: StorageChoice?
    public var serverAddress = ""
    public var bucketURL = ""
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

    private let service: FirstRunService
    private let existing: Set<String>

    public init(service: FirstRunService, at start: FirstRunStep = .welcome, existing: [String] = [], fromBoxList: Bool = false) {
        self.service = service
        self.existing = Set(existing)
        self.fromBoxList = fromBoxList
        step = start
        steps = start == .firstBox ? [.firstBox, .gettingReady] : FirstRunStep.allCases
        if start == .firstBox { storage = .thisMac }
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
            switch storage {
            case .server: return !trimmed(serverAddress).isEmpty
            case .bucket: return !trimmed(bucketURL).isEmpty
            case .thisMac: return true
            case nil: return false
            }
        case .firstBox:
            return !boxName.isEmpty && nameProblem == nil
        case .gettingReady:
            return false
        }
    }

    /// Why Continue can't go on from this step, in one line; nil while it
    /// can (or while a press is already working). The disabled button says
    /// it to VoiceOver, and a press (Return) shows it.
    public var whyNot: String? {
        guard !busy, !canContinue else { return nil }
        switch step {
        case .welcome, .gettingReady:
            return nil
        case .recoveryKey:
            return recoveryKey == nil ? FirstRunText.noKeyYet : FirstRunText.tickSaved
        case .storage:
            switch storage {
            case .server: return FirstRunText.typeServer
            case .bucket: return FirstRunText.typeBucket
            case .thisMac, nil: return FirstRunText.chooseStorage
            }
        case .firstBox:
            return nameProblem ?? FirstRunText.typeName
        }
    }

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
            // Busy: the press before is still working, and shows it.
            if let why = whyNot { refuse(notice: why) }
            return
        }
        busy = true
        defer { busy = false }
        do {
            switch step {
            case .welcome where recoveryKey == nil:
                recoveryKey = try await service.makeRecoveryKey()
            case .firstBox:
                try await service.createBox(NewBox(
                    name: boxName,
                    storage: storage ?? .thisMac,
                    serverAddress: storage == .server ? trimmed(serverAddress) : "",
                    bucketURL: storage == .bucket ? trimmed(bucketURL) : ""
                ))
                made = boxName
            default:
                break
            }
            error = nil
            notice = nil
            step = steps[i + 1]
        } catch let failure as FirstRunError where failure.notBuilt {
            refuse(notice: failure.message)
        } catch {
            refuse(error: (error as? LocalizedError)?.errorDescription ?? error.localizedDescription)
        }
    }

    private func refuse(notice: String? = nil, error: String? = nil) {
        self.notice = notice
        self.error = error
        refusals += 1
    }

    private func trimmed(_ s: String) -> String { s.trimmingCharacters(in: .whitespacesAndNewlines) }
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

    /// Why Continue can't go on yet (R-0126: never a silent press).
    public static let noKeyYet = "Your recovery key isn\u{2019}t ready yet."
    public static let tickSaved = "Tick \u{201C}I\u{2019}ve saved my recovery key somewhere safe\u{201D} to continue."
    public static let chooseStorage = "Choose where your saves should go."
    public static let typeServer = "Type your server\u{2019}s address."
    public static let typeBucket = "Type your bucket\u{2019}s address."
    public static let typeName = "Give your workspace a name."

    public static let storageTitle = "Where should your saves go?"
    public static let storageServer = "My own server"
    public static let storageBucket = "An S3-compatible bucket"
    public static let storageThisMac = "This Mac only"
    public static let thisMacOnly = "Your saves will only exist on this Mac, so a lost or broken Mac loses them. You can add a server or bucket later in Settings › Storage."

    public static let firstBoxTitle = "Make your first box"
    public static let firstBoxLine = "A box is an encrypted workspace for one project. Give it a short name, like the project’s."

    /// New Box… from the box list (the new-box board).
    public static let newBoxTitle = "New box"
    public static let createBox = "Create Box"
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
            return "There\u{2019}s already a box called \u{201C}\(name)\u{201D}."
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
        "once a week: whether a box moved, whether an agent worked in a box, and rough counts of your boxes and servers (like 2–5)",
    ]
    public static let neverCounted = "Never sent: any ID, your name or email, box names, file names or anything in your boxes. Your IP address is seen in transit but never kept."
    public static let whatsCounted = "What’s counted"
    public static let turnOff = "You can turn this off at any time in Settings › Privacy. The update check itself stays, for security updates."
}
