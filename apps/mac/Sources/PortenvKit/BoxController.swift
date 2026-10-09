// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// The box's save state as portenvd records it (`portenv app state`): the
/// only source of what the title says about saving (docs/PLAN.md, 1.4).
public struct BoxState: Equatable, Sendable {
    public enum Save: String, Sendable {
        case notSavedYet = "SAVE_STATE_NOT_SAVED_YET"
        case saving = "SAVE_STATE_SAVING"
        case saved = "SAVE_STATE_SAVED"
        case offline = "SAVE_STATE_OFFLINE"
        case agentUnavailable = "SAVE_STATE_AGENT_UNAVAILABLE"
        case closed = "SAVE_STATE_CLOSED"
        case retrying = "SAVE_STATE_RETRYING"
        case notSaved = "SAVE_STATE_NOT_SAVED"
        case quitUnsaved = "SAVE_STATE_QUIT_UNSAVED"
    }

    public var save: Save
    public var savedAt: Date?
    /// The server the box runs on; nil for this Mac.
    public var location: String?
    /// Packages from apt-packages.txt that couldn't be installed (the
    /// inspector's Where it is section; never the state line).
    public var failedPackages: [String] = []

    public init(save: Save, savedAt: Date? = nil, location: String? = nil) {
        self.save = save
        self.savedAt = savedAt
        self.location = location
    }

    /// Parses `portenv app state` output.
    public static func parse(_ json: String) -> BoxState? {
        struct Wire: Decodable {
            let state: String
            let saved_at: Date?
            let location: String?
            let failed_packages: [String]?
        }
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = f.date(from: s) { return date }
            f.formatOptions = [.withInternetDateTime]
            if let date = f.date(from: s) { return date }
            throw DecodingError.dataCorruptedError(in: try dec.singleValueContainer(), debugDescription: "date")
        }
        guard let w = try? d.decode(Wire.self, from: Data(json.utf8)), let save = Save(rawValue: w.state) else { return nil }
        var s = BoxState(save: save, savedAt: w.saved_at, location: (w.location?.isEmpty ?? true) ? nil : w.location)
        s.failedPackages = w.failed_packages ?? []
        return s
    }

}

/// One box's window: where the box is, what the title's state line says,
/// and the box actions. Every action goes through portenvd.
@MainActor
@Observable
public final class BoxController {
    /// Where the box is open.
    public enum Location: Equatable, Sendable {
        case closed
        case thisMac
        case server(String)
    }

    public let box: String
    public private(set) var location: Location = .closed
    public private(set) var state: BoxState?
    /// Opening or moving, which are not save states; nil otherwise.
    public private(set) var progress: String? = "Opening…"
    public private(set) var servers: [String] = []
    public private(set) var busy = false
    /// The last failure, shown in a sheet; cleared by dismissError.
    public private(set) var error: String?
    /// Changes whenever the box starts again (here or on a server), so the
    /// terminal reattaches to the new start.
    public private(set) var terminalGeneration = 0

    private let cli: CLIRunning
    private let keychain: KeychainReading
    private let notifier: KeychainWaitNotifying?
    private let keychainThreshold: Duration

    /// macOS is (or may be) asking the person to approve a Keychain read:
    /// the window says so and offers Cancel.
    public private(set) var waitingForKeychain = false
    /// A secondary line under a progress line (the Keychain wait's hint).
    public private(set) var progressDetail: String?
    private var cancelKeychain: (@MainActor () -> Void)?

    public init(box: String, cli: CLIRunning, keychain: KeychainReading = SystemKeychain(),
                notifier: KeychainWaitNotifying? = nil, keychainThreshold: Duration = KeychainWait.threshold) {
        self.box = box
        self.cli = cli
        self.keychain = keychain
        self.notifier = notifier
        self.keychainThreshold = keychainThreshold
    }

    /// A message shown for StateLine.transientSeconds (a revert, a move
    /// finished), then the normal line again.
    public private(set) var transient: String?
    private var transientTask: Task<Void, Never>?

    /// The title's state line: progress while opening or moving, a transient
    /// message, otherwise the recorded save state only.
    public var subtitle: String { progress ?? transient ?? state?.line() ?? "" }

    /// The title's look from one snapshot: the state line, the symbol and
    /// whether it spins always agree (§3.1).
    public var title: TitleLook { TitleLook(line: subtitle, symbol: symbol, spins: symbolSpins) }

    /// The sync symbol next to the title (§3.1).
    public var symbol: String? {
        if waitingForKeychain { return "lock" }
        return progress != nil ? "arrow.triangle.2.circlepath" : transient != nil ? "checkmark.circle" : state?.symbol
    }

    /// Whether the symbol animates (the view turns it off with Reduce Motion).
    public var symbolSpins: Bool {
        !waitingForKeychain && (progress != nil || (transient == nil && state?.symbolSpins == true))
    }

    /// The box's menu (§3.2), for the title menu and the menu bar's Box menu.
    public func menuItems(optionHeld: Bool) -> [BoxMenuItem] {
        let here: String? = switch location {
        case .thisMac: "this-mac"
        case .server(let s): s
        case .closed: nil
        }
        return BoxMenuModel.items(open: isOpen, busy: busy, agentUnavailable: agentUnavailable,
                                  optionHeld: optionHeld, location: here, servers: servers)
    }

    /// Runs a menu item's action.
    public func run(_ action: BoxMenuItem.Action) async {
        switch action {
        case .none: break
        case .move(let target): await move(to: target)
        case .revertToLastSavePoint: await revertToLastSavePoint()
        case .makeSavePoint: await makeSavePoint()
        case .restartBox: await restartBox()
        }
    }

    private func show(_ message: String) {
        transient = message
        transientTask?.cancel()
        transientTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(StateLine.transientSeconds))
            guard !Task.isCancelled else { return }
            self?.transient = nil
        }
    }

    /// The box agent stopped answering: nothing can be saved, and unsaved
    /// changes stay in the box until Restart Box.
    public var agentUnavailable: Bool { state?.save == .agentUnavailable }

    /// The box is open somewhere this window can act on.
    public var isOpen: Bool { location != .closed }

    /// Whether a Move To target is where the box runs (shown checked).
    public func isCurrent(_ target: String) -> Bool {
        switch location {
        case .thisMac: return target == "this-mac"
        case .server(let s): return target == s
        case .closed: return false
        }
    }

    /// Opens the box (on this Mac, or shows it where it runs).
    public func open() async {
        progress = "Opening…"
        await perform {
            self.servers = (try? await self.cli.run(["app", "servers", self.box]))?
                .split(separator: "\n").map(String.init) ?? []
            do {
                _ = try await self.cli.run(["app", "open", self.box])
            } catch let e as CLIError where e.message.contains(KeychainWait.daemonError) {
                // portenvd never prompts: read the keys here, in front of
                // the person, and hand them over. Cancelled: nothing opened.
                guard try await self.approveKeys() else {
                    self.location = .closed
                    return
                }
                _ = try await self.cli.run(["app", "open", self.box])
            }
            self.terminalGeneration += 1
        }
        progress = nil
        progressDetail = nil
        await refresh()
    }

    /// Cancel while macOS asks to approve the Keychain read: the open stops
    /// before portenvd starts anything (no box, no lease). The read itself
    /// ends whenever the prompt is answered; its result is dropped.
    public func cancelKeychainWait() { cancelKeychain?() }

    /// Reads the box's keys from the Keychain off the main thread and hands
    /// them to portenvd. After keychainThreshold the window says it's
    /// waiting (and a notification goes out); no timeout while the person
    /// might be answering. Returns false when cancelled.
    private func approveKeys() async throws -> Bool {
        let ids = try await cli.run(["app", "key-ids", box]).split(separator: "\n").map(String.init)
        let reader = keychain
        let once = Once<Result<[String: Data], Error>?>()
        let result = await withCheckedContinuation { (done: CheckedContinuation<Result<[String: Data], Error>?, Never>) in
            once.set { done.resume(returning: $0) }
            cancelKeychain = { once.resume(nil) }
            Task.detached {
                do {
                    var keys: [String: Data] = [:]
                    for id in ids {
                        if let key = try reader.read(account: id) { keys[id] = key }
                    }
                    once.resume(.success(keys))
                } catch {
                    once.resume(.failure(error))
                }
            }
            Task { @MainActor [keychainThreshold, notifier, box] in
                try? await Task.sleep(for: keychainThreshold)
                guard !once.isDone else { return }
                self.waitingForKeychain = true
                self.progress = KeychainWait.line
                self.progressDetail = KeychainWait.detail
                await notifier?.keychainWaiting(box: box)
            }
        }
        waitingForKeychain = false
        progressDetail = nil
        cancelKeychain = nil
        guard let result else { return false }
        let keys = try result.get()
        let json = try JSONSerialization.data(withJSONObject: keys.mapValues { $0.base64EncodedString() })
        _ = try await cli.run(["app", "provide-keys", box], input: json)
        return true
    }

    /// Reads the recorded state from portenvd.
    public func refresh() async {
        guard let out = try? await cli.run(["app", "state", box]), let st = BoxState.parse(out) else { return }
        state = st
        switch st.save {
        case .closed: location = .closed
        default: location = st.location.map { .server($0) } ?? .thisMac
        }
    }

    /// File › Make Save Point (⌘S).
    public func makeSavePoint() async {
        guard isOpen else { return }
        await perform { _ = try await self.cli.run(["app", "point", self.box]) }
        await refresh()
    }

    /// Revert To ▸ Last Save Point, wherever the box runs. The home as it
    /// was is saved first.
    public func revertToLastSavePoint() async {
        guard isOpen else { return }
        await perform {
            let out = try await self.cli.run(["app", "revert", self.box])
            // "…; save point time 2026-10-08T12:31:00Z": say which save
            // point, in local time, for a few seconds.
            if let r = out.range(of: "save point time ") {
                let iso = String(out[r.upperBound...]).trimmingCharacters(in: .whitespacesAndNewlines)
                if let t = ISO8601DateFormatter().date(from: iso) { self.show(StateLine.reverted(to: t)) }
            }
        }
        await refresh()
    }

    /// Move To ▸ This Mac or a server. The current location is a no-op.
    public func move(to target: String) async {
        guard !isCurrent(target) else { return }
        progress = StateLine.moving(to: target)
        await perform {
            _ = try await self.cli.run(["app", "move", self.box, target])
            self.terminalGeneration += 1
            self.show(StateLine.moved(to: target))
        }
        progress = nil
        await refresh()
    }

    /// Closing the window saves and releases a box open on this Mac.
    public func close() async {
        guard location == .thisMac else { return }
        await perform { _ = try await self.cli.run(["app", "close", self.box]) }
        location = .closed
    }

    public func dismissError() { error = nil }

    /// What quitting needs: this box, and whether it is open on this Mac.
    public var quitFlow: QuitFlow { QuitFlow(cli: cli, box: box, openHere: location == .thisMac) }

    /// The Mac woke from sleep: portenvd checks the box agent's channel and
    /// restarts a box whose channel is gone; reconnect the terminal then.
    public func woke() async {
        guard isOpen else { return }
        if let out = try? await cli.run(["app", "woke"]), out.contains("restarted") {
            terminalGeneration += 1
        }
        await refresh()
    }

    /// The terminal's connection ended without the user leaving it: ask
    /// portenvd whether the box agent still answers; reattach if it does.
    public func terminalEnded() async {
        guard isOpen, !busy else { return }
        if (try? await cli.run(["app", "check", box])) != nil {
            terminalGeneration += 1
        }
        await refresh()
    }

    /// Restart Box: stops the box and opens it again where it runs; the home
    /// and its unsaved changes stay (the resume rules never restore over
    /// them).
    public func restartBox() async {
        guard isOpen else { return }
        await perform {
            _ = try await self.cli.run(["app", "restart", self.box])
            self.terminalGeneration += 1
        }
        await refresh()
    }

    private func perform(_ action: @escaping @MainActor () async throws -> Void) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            try await action()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
