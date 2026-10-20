// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The hosted classifier chosen in Settings › Experimental.
public enum ClassifierChoice: String, CaseIterable, Sendable {
    case rules, jev
    public var title: String {
        switch self {
        case .rules: "Built-in rules"
        case .jev: "jev (TypeSafe)"
        }
    }
}

/// The suggester chosen in Settings › Experimental.
public enum SuggesterChoice: String, CaseIterable, Sendable {
    case anthropic, none
    public var title: String {
        switch self {
        case .anthropic: "Anthropic"
        case .none: "None"
        }
    }
}

/// Settings › Experimental's values (ADR 0027 §0, S5a). Smart input is off
/// by default; off is today's terminal exactly. No key is kept here: keys
/// are in the Keychain only.
public struct SmartInputSettings: @unchecked Sendable {
    private let defaults: UserDefaults
    public init(defaults: UserDefaults = .standard) { self.defaults = defaults }

    enum Key {
        static let enabled = "SmartInput.enabled"
        static let classifier = "SmartInput.classifier"
        static let suggester = "SmartInput.suggester"
        static let anthropicModel = "SmartInput.anthropicModel"
        static let jevModel = "SmartInput.jevModel"
        static let jevThreshold = "SmartInput.jevThreshold"
    }

    public var enabled: Bool {
        get { defaults.bool(forKey: Key.enabled) }
        nonmutating set { defaults.set(newValue, forKey: Key.enabled) }
    }

    public var classifier: ClassifierChoice {
        get { defaults.string(forKey: Key.classifier).flatMap(ClassifierChoice.init(rawValue:)) ?? .rules }
        nonmutating set { defaults.set(newValue.rawValue, forKey: Key.classifier) }
    }

    public var suggester: SuggesterChoice {
        get { defaults.string(forKey: Key.suggester).flatMap(SuggesterChoice.init(rawValue:)) ?? .anthropic }
        nonmutating set { defaults.set(newValue.rawValue, forKey: Key.suggester) }
    }

    public var anthropicModel: String {
        get { Self.nonEmpty(defaults.string(forKey: Key.anthropicModel)) ?? AnthropicSuggester.defaultModel }
        nonmutating set { defaults.set(newValue.trimmingCharacters(in: .whitespaces), forKey: Key.anthropicModel) }
    }

    public var jevModel: String {
        get { Self.nonEmpty(defaults.string(forKey: Key.jevModel)) ?? JevClassifier.defaultModel }
        nonmutating set { defaults.set(newValue.trimmingCharacters(in: .whitespaces), forKey: Key.jevModel) }
    }

    /// 0.5 to 1; 0.8 to start.
    public var jevThreshold: Double {
        get {
            let v = defaults.double(forKey: Key.jevThreshold)
            return (0.5 ... 1).contains(v) ? v : JevClassifier.defaultThreshold
        }
        nonmutating set { defaults.set(min(1, max(0.5, newValue)), forKey: Key.jevThreshold) }
    }

    private static func nonEmpty(_ s: String?) -> String? {
        guard let s, !s.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        return s
    }
}

/// Builds the adapters from the settings. Building one reads no key: an
/// adapter reads its key only when it makes a call.
public enum SmartInputSetup {
    /// The adapters' session: ephemeral, so no request or answer is cached
    /// on disk, and no cookies.
    public static func sessionConfiguration() -> URLSessionConfiguration {
        let c = URLSessionConfiguration.ephemeral
        c.urlCache = nil
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.waitsForConnectivity = false
        return c
    }

    public static let session = URLSession(configuration: sessionConfiguration())

    /// The hosted classifier, or nil for the built-in rules alone.
    public static func classifier(_ s: SmartInputSettings, keys: any ProviderKeyStore, session: URLSession = session) -> (any Classifier)? {
        switch s.classifier {
        case .rules: nil
        case .jev: JevClassifier(keys: keys, model: s.jevModel, threshold: s.jevThreshold, session: session)
        }
    }

    public static func suggester(_ s: SmartInputSettings, keys: any ProviderKeyStore, session: URLSession = session,
                                 commands: [PortenvCommandSummary]) -> (any Suggester)? {
        switch s.suggester {
        case .anthropic: AnthropicSuggester(keys: keys, model: s.anthropicModel, commands: commands, session: session)
        case .none: nil
        }
    }

    /// The app's runner: the toggle read on every Return, the Keychain's
    /// keys, the counter in the app's defaults. (S1's editor and S4 call
    /// `decide`; until they land, nothing in the terminal does.)
    public static func runner(defaults: UserDefaults = .standard, commands: [PortenvCommandSummary] = [],
                              usage: SmartInputUsage) -> SmartInputRunner {
        let s = SmartInputSettings(defaults: defaults)
        let keys = KeychainProviderKeys()
        return SmartInputRunner(enabled: { s.enabled }, classifier: classifier(s, keys: keys), suggester: suggester(s, keys: keys, commands: commands),
                                usage: usage)
    }
}

/// The adapters' one HTTP call: a JSON POST, its body on 2xx; any other
/// status is `.http`, no answer is `.transport`. Neither keeps the
/// response body, so no error ever carries what the service echoed.
enum SmartInputHTTP {
    static func send(_ req: URLRequest, on session: URLSession) async throws -> Data {
        let data: Data, resp: URLResponse
        do { (data, resp) = try await session.data(for: req) } catch { throw AdapterFailure.transport }
        guard let http = resp as? HTTPURLResponse else { throw AdapterFailure.transport }
        guard (200 ..< 300).contains(http.statusCode) else { throw AdapterFailure.http(http.statusCode) }
        return data
    }
}

/// Settings › Experimental's copy (GUIDELINES §9.1).
public enum SmartInputText {
    public static let heading = "Experimental"
    public static let smartInput = "Smart input"
    public static let smartInputLine = "Type a request in plain words at a shell prompt and get a command back to check before it runs. Off, the terminal is exactly as it was."
    public static let classifierLabel = "Recognising requests"
    public static let classifierLine = "Built-in rules work on this Mac. jev also checks lines the rules can\u{2019}t place, and gets only the typed line and the shell\u{2019}s name."
    public static let suggesterLabel = "Suggestions"
    public static let suggesterLine = "Gets the request, the shell\u{2019}s name, Portenv\u{2019}s commands and your agents\u{2019} names and roles. Never files, messages, output or history."
    public static let model = "Model"
    public static let threshold = "Confidence needed"
    public static let keysHeading = "Keys"
    public static let keysFooter = "Keys are kept in this Mac\u{2019}s Keychain and used only by this app. Portenv\u{2019}s background service, the workspace and agents never see them."
    public static let keyPlaceholder = "Paste a key"
    public static let saveKey = "Save"
    public static let removeKey = "Remove"
    public static let notAKey = "That doesn\u{2019}t look like a key. Paste it again, without spaces."
    public static let keychainRefused = "The Keychain didn\u{2019}t save the key. Try again."
    public static let usageHeading = "Usage on this Mac"
    public static let noUsage = "No requests yet."
    public static let resetUsage = "Reset"

    public static func keyLabel(_ id: ProviderKeyID) -> String {
        switch id {
        case .anthropic: "Anthropic API key"
        case .jev: "jev API key"
        }
    }

    public static func adapterName(_ id: String) -> String {
        switch id {
        case "anthropic": "Anthropic"
        case "jev": "jev"
        default: id
        }
    }

    /// A saved key is never shown back: the row says when it was saved.
    public static func keyState(savedOn: Date?, now: Date = Date(), timeZone: TimeZone = .current, locale: Locale = SettingsText.appLocale()) -> String {
        guard let savedOn else { return "Not set" }
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = timeZone
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = timeZone
        f.dateFormat = cal.isDate(savedOn, equalTo: now, toGranularity: .year) ? "d MMM" : "d MMM yyyy"
        return "Saved on \(f.string(from: savedOn))"
    }

    /// One adapter's usage line.
    public static func usage(_ c: AdapterCounts) -> String {
        var parts = ["\(c.requests) requests", "\(c.fallbacks) fell through", "\(c.failures) failed"]
        if let p50 = c.p50, let p95 = c.p95 { parts.append("\(p50) ms typical, \(p95) ms slowest 5%") }
        parts.append("\(c.inputTokens) tokens in, \(c.outputTokens) out")
        if let m = c.model { parts.append(m) }
        return parts.joined(separator: " · ")
    }
}
