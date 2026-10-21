# 0027. Terminal input: an editor at the prompt, shell-integration blocks, local completions, and experimental natural language

Date: 2026-10-19 · Status: proposed, for the tester build. From the owner's decision R-0348 ("Warp-style terminal input") and amendments R-0349 to R-0356. Phase 1 ships with the tester build; phase 2 waits for the owner's go.

## Context

Most of what this needs exists:
- **OSC 133 hooks** (ADR 0017): bash from `/etc/profile.d` (every home), zsh from the skeleton's `.zshrc` (new homes only), emitting A, B, C and D;exit.
- **The helper's tracker** (`core/agent/osc133.go`) feeds ADR 0017's waiting detector and ADR 0020's mark replay.
- **The app** follows the same marks (`OverlayTracker`, which records each prompt's B column), builds `CommandBlock`s and draws blocks (`BlockCards`).
- **Input** goes through `TabTerminal` as the person, attributed by portenvd (ADR 0020 decision 4). Since #216, a paste goes to tmux's `paste-buffer -p`, bracketed only if the program asked.
- **The CLI** has two sets of commands:
  - the owner's, on the Mac: `status`, `attach`, `app …`;
  - an agent's, in a workspace through its grant socket: `msg`, `tab new`, `team status`, `inbox`, …

GUIDELINES §14 lists "a custom input editor" and "a built-in AI assistant" as things Portenv doesn't do. This ADR proposes changing the first and keeping the second (see "Settled text").

## Decision

### 0. Phases, and the off state

- **Phase 1 (the tester build, R-0353):** smart input behind one experimental toggle, Settings › Experimental › "Smart input", **off by default**. No Portenv backend: hosted adapters use keys the owner stores in the Keychain.
- **Phase 2 (only after the owner says phase 1 works):** a Portenv cloud adapter, with a backend and dashboard in the private cloud repository (section 6).
- **Off means a normal terminal (R-0352).** With the toggle off, input looks and behaves exactly like today's terminal. Turning it on adds only a ✦ marker at the prompt and tinted inline suggestions: no new panels and no layout change. The shell-integration hooks (section 2) are plumbing and run either way.

### 1. The input editor (stream S1)

When smart input is on, the input line at a shell prompt is an in-app editor: an `NSTextView` over the prompt row from B's column, drawn in the terminal's font and colours.
- **Editing:** multi-line, mouse selection, undo and redo, macOS text editing, IME, and VoiceOver ("Command input, <tab>").
- **Keys:**
  - Return submits.
  - ⇧Return and ⌥Return insert a new line.
  - Tab or → at the end accepts the tinted suggestion. Escape dismisses it.
  - ⌃C clears the line (undoable). ⌃D on an empty line goes to the shell.
  - ↑ on the first line, ⌃R, and any key the editor doesn't handle **hand over**: the text goes to the shell's own line as a one-line paste, then the key. The editor stays off until the next prompt.
- **On only when all of these hold:**
  - the tab's last mark is A or B in the current epoch;
  - the alternate screen is off (not vim, less, an ssh session's full-screen program, or an agent TUI such as Claude Code);
  - the tab's own shell is the foreground process;
  - the marks are known (not after a `skipped` reset, and integration isn't off);
  - the tab is the person's;
  - the toggle is on.

  Otherwise the tab is the plain terminal, exactly as today.
- **Sending:** only at the same prompt the editor opened on, and only if no other author has typed since.
  - **One line:** its bytes, then CR.
  - **Several lines:** one paste through #216's path, then CR, and only when the helper saw `ESC[?2004h` after this prompt's A. Otherwise the text stays, with "This shell isn't taking multi-line input. Join the lines, or turn off smart input."
  - CRLF becomes LF.
- **Agents:**
  - Agent tabs never have the editor.
  - It sends only as the person, with portenvd's author, so it never types for an agent.
  - If another author types at the prompt (stand-in mode, a CLI attach, a routine), the editor hides until the next prompt and never sends over their text.
  - Its text and suggestions stay in the app; agents never see them.

### 2. Shell-integration blocks (S1)

Reused, with no second parser: the hooks, the helper's tracker and replay, `OverlayTracker`, `CommandBlock`s, the block UI, and ADR 0017's detector.

New:
- **zsh for every home,** from `/etc/zsh/zshrc`.
- **fish,** from `/etc/fish/conf.d/portenv-osc133.fish`, on fish's events. fish isn't in the image, so this is for people who add it.
- **Each hook** sets a guard variable, so sourcing it twice does nothing, and it skips marks the prompt already prints.
- **The hooks live in system files, never in the person's dotfiles.** A dotfile can only break them by replacing `PROMPT_COMMAND` or PS0. Then the tab has no marks, so blocks and the editor are off, and Settings › Terminal offers Repair: one guarded line at the end of `~/.bashrc` or `~/.zshrc`, added once, on the owner's click.
- **Opt-out:** `/home/work/.portenv/shell-integration` holding `off`. ADR 0017's detector then runs on its other three signals.
- **Bracketed paste:** the helper reports, per prompt, whether the shell turned it on. The bash hook turns readline's on.

### 3. Local completions (S2)

Completions come from specs, generators, history, the project's scripts and the team's names, and are shown as tinted inline text. Nothing on this path uses the network.
- **Specs:** the Fig-style open-source spec set (`withfig/autocomplete`).
  - It's MIT by its repository's licence metadata (checked 2026-10-19), so it fits Apache-2.0 with its notice.
  - It's vendored at a pinned commit, converted to declarative JSON at build time, and updated only with the app.
  - Parts that need JavaScript are dropped, so no third-party code runs.
- **Warp (R-0363):** its client is AGPL v3 (MIT only for its UI crates), so no Warp code is copied, ported or translated into Portenv, and lanes don't read its source while writing S1 or S2. Its public docs and behaviour are fine as reference; the Fig specs above stay as planned.
- **Generators:** a fixed, reviewed list, called by ID (`RunGenerator(tab, id, prefix)` on the helper, relayed by portenvd).
  - No command text crosses from the app.
  - They're read-only and never run project code: scripts come from parsing `package.json` and the Makefile, and git runs with fsmonitor and hooks off.
  - They run as the tab's user, with no stdin and no terminal.
  - They're killed at 500 ms or 64 KB.
- **History** comes from the shell's history file and this session's blocks, ranked in memory.
  - It's never written to the Mac's disk and never sent anywhere.
  - Entries that look like secrets aren't offered.

### 4. English to a command (S4; S5a for phase 1's keys)

**R-0349: it's a command unless shown otherwise.** **R-0354: every layer falls through.**
- **When a layer gives up:** any error, timeout, missing key, missing adapter or malformed answer skips that layer, and the next one down is used. The bottom is the plain terminal line.
- **What each failure looks like:**
  - a failed completer means no suggestion, and typing still works;
  - a failed classifier means the built-in rules decide, and if they're unsure, the line is a command;
  - a failed suggester means the line is a command.
- **Never an error dialog, never a blocked keystroke.** Keys typed while a layer is working go into the editor.

**The order on Return:**
1. **Smart input off, or the editor isn't showing:** the line is sent at once.
2. **S3's parser:** a `portenv` command (section 5) runs through portenvd.
3. **The built-in rules,** on the Mac, under 5 ms, always present, and the default classifier. A line is `shell` if it:
   - starts with a known program (the spec set's names, shell builtins, or first words from history);
   - starts with a path;
   - holds shell syntax (pipes, redirects, `&&`, `;`, assignments, quotes, substitutions).

   These lines are sent at once and **never reach a hosted classifier**. The rules call a line `english` only on strong signs (several words, no shell syntax, a question mark or a request verb). Anything else is unsure.
4. **Unsure, and a hosted classifier is configured:** it gets at most **150 ms**. Below its confidence threshold, or past the budget, the line is a command.
   - Only lines the rules couldn't place wait at all, and 150 ms is short enough not to feel like lag after Return.
   - The final value waits on measured latency (4a).
5. **`english` or `portenv`:** the line is held, dimmed in place, and the suggester gets at most **2 s**.
   - Past 2 s the person has likely moved on, and a one-line answer from a hosted model usually comes well within it. To be measured in phase 1.
   - **An answer** replaces the text as one undo step, tinted and marked ✦. Nothing runs until Return.
   - **No answer** (a failure or the timeout): the text is shown as typed, with no message. The next Return sends it without asking again. Nothing ever runs on its own.

**The circuit breaker,** per adapter:
- three failures in a row (an error, a timeout or garbage) skip that adapter for 60 s;
- each further failure after a cool-down doubles the skip, up to 15 min;
- a success resets it.

The breaker keeps a dead service from adding its timeout to every line.

**Context sent (R-0350), and only this:**
- the sentence and the shell's name;
- the command list (section 7);
- each team agent's ID, name and role;
- **with an opt-in, off by default:** the project's script names (`package.json` script keys and Makefile targets, parsed, never run; at most 100).

Never sent: file contents, messages, decisions' text, output, the screen, history, environment, secrets, file or directory names, and runbooks.
- **Redaction first:** each field passes ADR 0017's secret words plus token shapes, and each match becomes `<secret>`.
- A sentence over 500 characters isn't sent.
- Each adapter may send less (4a).

**The answer** is one line of at most 2 KB, cleaned like a paste. It's a shell command or a Portenv command, for example "wake the writer" → `portenv wake writer`, or "deploy to staging" → `portenv deploy staging`. It only fills the editor.

**Phase 1's keys (S5a, R-0353):**
- **Where they live:** hosted adapters' keys sit in the Keychain, entered by the owner in Settings › Experimental. The default suggester is Anthropic's; any provider is allowed for testing.
- **Who reads them:** the app reads a key only when it makes a call.
  - It's never logged, never in the repository or any board, and never readable by agents, portenvd or the helper.
  - Only the app, in front of the person, may raise the Keychain prompt (CLAUDE.md).
- **The usage counter:** Settings shows a local counter per adapter: requests, fallbacks, failures, p50 and p95 latency, and input and output tokens. Phase 2 can be compared against it.

**Agents never use this path.** It fills the person's editor and never acts, so it isn't an agent of Portenv's own (ADR 0006).

### 4a. Classifier adapter: jev (R-0355)

Jev, TypeSafe's hosted "System One" model, answers typed questions with probabilities and generates no text, so it fits the Classifier interface.
- **The call:** `POST https://api.typesafe.ai/v1/systemone`, made with plain `URLSession` and no SDK.
  - **Headers:** `Authorization: Bearer <key from the Keychain>` and `Content-Type: application/json`.
  - **Body:** `model` is a setting, `jev-latest` by default; tests pin a version, because answers change between releases. `state` is the typed line and the shell's name, and nothing else.
  - **One question,** `intent`, of type `choice`, with these `criteria`:
    - `shell`: "a shell command to run as typed";
    - `english`: "a plain-language request for a command";
    - `portenv`: "a request about the Portenv team, agents, tabs or deploys".
- **The answer:** `answers.intent.choice` is used only if `answers.intent.confidence` is at least the threshold (a setting, 0.8 to start).
  - Below the threshold, or a missing or malformed answer, or past the 150 ms budget: the built-in rules decide, then the line is a command.
  - The circuit breaker above applies.
- **Usage:** `usage.input_tokens` and `usage.output_tokens` go into the local counter, and so does the answering `model`.
- **The rules come first:** a line that starts with a known program or a path, or holds shell syntax, never calls jev.
- **The budget isn't final yet:** real latency is measured with the owner's key and reported before the 150 ms budget is fixed.

### 5. The CLI surface (S3)

Owner commands on the Mac, through portenvd, named consistently with today's:

| Command | Today |
| --- | --- |
| `portenv msg AGENT TEXT [--thread ID]` | new for the owner (agents have it); portenvd's `SendMessage` exists |
| `portenv wake AGENT` | new; the helper's `WakeAgent` exists, and portenvd gains an owner call |
| `portenv tab new --name NAME [-- COMMAND…]` | new for the owner (agents have it); `NewTab` exists |
| `portenv team start [AGENT…]`, `portenv team stop [AGENT…]` | new; `StartAgent` and `StopAgent` exist |
| `portenv deploy TARGET` | new, once ADR 0026 (#224) is accepted. With no runbook it refuses: "This project has no deploy runbook, so Portenv won't guess how to deploy. Add DEPLOY.md (see `portenv guide`), then try again." |
| `portenv decision list` | new; lists what's waiting and points to Needs you in the app. It never answers (below) |
| `portenv status` | exists |

**The CLI gets no extra powers: portenvd decides.**
- Each command is one portenvd call, the same one the UI makes.
- The caller is fixed when the connection is accepted: the owner on the owner-only socket, an agent by its grant.
- Approvals appear only in the app. `deploy production` waits on the same sheet as the button.
- No flag skips an approval.
- **Decisions stay app-only (ADR 0022, R-0370).** No command answers a decision: the CLI points to Needs you, and there's no `decision answer` and no `answer` alias for decisions (PLAN's `portenv answer` is the tab relay, not this). Smart input may suggest "open Needs you", never an answer.
- The workspace's shell (`work`) has no owner socket, so these commands there are refused, as they are today.

**From the editor:** a `portenv` line whose verb is in this list is marked ✦ "Runs in Portenv". Return runs it through portenvd as the app's action, never in the shell. `command portenv …` reaches the shell's own.

### 6. Phase 2: the Portenv cloud adapter (S5, S6; after the owner's go)

**One call.** In phase 2 the app talks only to Portenv's own API, through a "Portenv cloud" adapter that classifies and suggests in one call (R-0356).
- The jev and model keys live only on the server, in a secrets manager, never in either repository. Per-user metering happens there.
- The built-in rules still run on the Mac first.
- If the API is down, the line is a command.
- The direct-key adapters stay for phase 1 and for development.

**The client contract** (the part that belongs in this repository): `POST /v1/suggest` on Portenv's API.

```json
{"schema": "portenv.suggest/1", "request_id": "…", "line": "wake the writer", "shell": "zsh",
 "commands_version": "1.4.0", "team": [{"id": "writer", "name": "Writer", "role": "Code Writer"}],
 "scripts": ["test", "build"]}
```

`scripts` is present only with the opt-in. The response:

```json
{"request_id": "…", "intent": "portenv", "confidence": 0.93, "command": "portenv wake writer"}
```

`command` is null when the intent is `shell` or there's no answer.

| Response | What the app does |
| --- | --- |
| 200 | Uses the answer under section 4's rules |
| 401 | Not signed in or the token is refused: smart input's English layer is unavailable, and lines are commands |
| 429 | Rate limited: the line is a command |
| 503 `{"error": "suggestions_off"}` | The kill switch: the line is a command, and the app doesn't ask again for 10 min |
| Other, or no answer within 2 s | The line is a command, and the circuit breaker counts it |

**Auth:** `Authorization: Bearer <token>` from the GitHub sign-in the app already has (the device flow, ADR 0025, #200).
- The backend verifies the token with GitHub and keeps only the GitHub user ID.
- Without sign-in, the toggle's English layer isn't offered.
- The backend has a per-user rate limit and a global daily usage cap; past the cap it fails closed, so lines are commands.
- A kill switch turns suggestions off for everyone.

**The privacy note,** shown in the app before a person turns phase 2 on:
- **What leaves the Mac:** the sentence and section 4's context, plus the GitHub token, from which the backend takes the user ID.
- **What reaches the model:** the backend passes the sentence and context to a hosted LLM for that one request.
- **What's kept:** per-user aggregate counts only (requests, tokens, fallbacks, latency), for usage reporting. No sentence or suggestion text is stored, and logs carry no prompt text.
- **Retention** of the counts is an open question.
- **The kill switch** turns suggestions off for everyone.

**Where it lives:** the backend (S5), the owner's dashboard (S6) and their infrastructure live in the private `portenv/cloud` repository. That covers its infrastructure-as-code choice, CDK or Terraform.
- The dashboard is for an allowlist of GitHub users only. It shows signed-in users, requests per day, error and fallback rates, latency, usage against the cap, and the kill switch.
- In this repository, S5 is only the adapter and this contract.

### 7. The contracts

The contracts are fixed so the streams can build in parallel. Each stream mocks the other side until it merges.

**InputLine** (S1 implements it; S2 and S4 consume it; PortenvKit):

```swift
public struct PromptID: Hashable, Sendable { public let line: Int; public let epoch: Int }
public enum ShellKind: String, Sendable { case bash, zsh, fish, unknown }

public struct InputSuggestion: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        case completion(replacing: Range<Int>, with: String)  // UTF-16 offsets at `revision`
        case wholeLine(String)                                // S4's answer, marked ✦
    }
    public let kind: Kind
    public let detail: String?                                // "Runs in Portenv", a description
}

public enum InputLineEvent: Equatable, Sendable {
    case activated(PromptID, ShellKind), edited(revision: Int)
    case accepted(offer: UUID), dismissed(offer: UUID)
    case submitted(PromptID), deactivated
}

public enum SubmitDecision: Sendable { case send, hold, runInPortenv }

@MainActor public protocol SubmitInterceptor: AnyObject {
    /// On Return, in order (S3, then S4). S1 waits at most `budget`; a late,
    /// throwing or missing answer is .send. Keys typed meanwhile go into the line.
    var budget: Duration { get }
    func decide(_ text: String, prompt: PromptID) async -> SubmitDecision
}

@MainActor public protocol InputLine: AnyObject {
    var tab: String { get }                  // "@N"
    var prompt: PromptID? { get }            // nil when inactive
    var text: String { get }                 // only S1 changes it
    var selection: Range<Int> { get }        // UTF-16; empty = cursor
    var revision: Int { get }                // bumps on every edit
    var events: AsyncStream<InputLineEvent> { get }
    /// Dropped if `revision` is stale; a newer offer from `source` replaces the older.
    @discardableResult func offer(_ s: InputSuggestion, at revision: Int, source: String) -> UUID
    func withdraw(_ offer: UUID)
    func addInterceptor(_ i: SubmitInterceptor)
}
```

- **Who owns the text:** only S1 changes it. A suggestion changes it only when the person accepts it, as one undo step.
- **Threading:** everything is on the main actor. Consumers do slow work off it.
- **Submitting:** S1 runs the interceptors, then sends through `TabTerminalSource.send`, or hands `.runInPortenv` to S3.
- **The mock:** `ScriptedInputLine`.

**Completer, Classifier and Suggester** (R-0354). App code talks only to these protocols. Each vendor is one adapter file, and the choice for each is in Settings › Experimental.

```swift
public struct InputContext: Sendable {        // built once per Return, already redacted (R-0350)
    public let line: String; public let shell: ShellKind
    public let commandsVersion: String; public let team: [TeamAgentRef]; public let scripts: [String]?
}
public enum Intent: String, Sendable { case shell, english, portenv }
public struct Classification: Sendable { public let intent: Intent; public let confidence: Double }
public struct AdapterUsage: Sendable { public var inputTokens = 0, outputTokens = 0; public var model: String? }

public protocol Completer: Sendable {          // S2; local only
    var id: String { get }
    func complete(_ line: String, cursor: Int, shell: ShellKind) async throws -> [InputSuggestion]
}
public protocol Classifier: Sendable {         // built-in rules (default), jev, …
    var id: String { get }
    func classify(_ c: InputContext) async throws -> (Classification?, AdapterUsage)  // nil = unsure
}
public protocol Suggester: Sendable {          // a hosted LLM, …
    var id: String { get }
    func suggest(_ c: InputContext, intent: Intent) async throws -> (String?, AdapterUsage)
}
/// One call that does both (phase 2's Portenv cloud adapter, R-0356).
public protocol ClassifyingSuggester: Classifier, Suggester {
    func classifyAndSuggest(_ c: InputContext) async throws -> (Classification?, String?, AdapterUsage)
}
```

- **One fall-through runner** (S4) owns the order, the budgets, the circuit breaker, the usage counter and the checks on answers. Adapters own none of these.
- **A swap, not a rewrite:** an adapter that conforms to `ClassifyingSuggester` is called once for both steps. That's how phase 1's direct keys become phase 2's backend.
- **Each adapter sends only the fields it needs.** jev sends the line and the shell's name.

**The command list** (S3 generates it from the CLI's registry; `portenv help --json`; bundled in the app):

```json
{"schema": "portenv.commands/1", "version": "1.4.0", "commands": [
  {"path": ["deploy"], "aliases": [], "summary": "Run the project's deploy runbook for a target.",
   "caller": ["owner", "agent"], "approval": "depends", "approval_note": "Production always asks in the app.",
   "args": [{"name": "target", "type": "runbook_target", "required": true, "repeated": false}],
   "flags": [], "since": "1.4.0"}]}
```

- **`type`** is one of `string`, `text`, `int`, `bool`, `path`, `agent_id`, `tab_id`, `decision_id`, `choice`, `runbook_target` or `command`.
- **`approval`** is `never`, `always` or `depends`. It's for display only; portenvd decides.
- **An unknown `schema` major** is refused, and the app falls back to no Portenv suggestions.
- **The mock:** a checked-in fixture. A test fails if the registry and the bundled file differ.

### 8. Streams

- **Each stream** writes its tests first, ships one PR per slice under the normal 200-line guard, and touches only its own files and the contract files. A contract changes only when the streams that use it agree.
- **Fall-through tests (R-0354):** every adapter has a failure test, a timeout test and a garbage-output test, each proving the fall-through.
- **Order:** S3 and S5a go first, since S4 needs both, and S1 and S2 run alongside. The tester build ships with S1–S4 and S5a. S5 and S6 wait for the owner's go on phase 1.

| Stream | Builds | First tests |
| --- | --- | --- |
| **S1** Editor and blocks | sections 0–2; InputLine | toggle off gives today's terminal, pixel for pixel; a heredoc reaches bash, zsh and fish intact; no editor in vim, less, ssh or an agent TUI; an agent at the prompt hides it; no send at a changed prompt; the hooks reach existing homes; opt-out and Repair; an interceptor that hangs or throws still sends; ADR 0020's latency gate holds |
| **S2** Completions | section 3, the `Completer` adapters | works with the network blocked; an unknown generator is refused; a hostile repository config runs nothing; generators are killed at 500 ms; a secret in history isn't offered; a completer that fails, times out or returns garbage shows nothing and typing goes on |
| **S3** CLI | section 5, the command list | each command on an agent's socket gets only its grant; `deploy production` waits on the app; no runbook refuses; `work` is refused; the registry matches the bundled file |
| **S4** English to a command | section 4, the built-in rules, the runner, the jev adapter (4a) | with smart input off, every key unavailable, or an adapter hanging, a known-program line's Return makes no network call and adds no delay; each classifier and suggester adapter, on failure, timeout and garbage, falls through to rules, then command; the breaker opens after three failures and cools down; request bodies hold only the listed fields (markers planted in files, messages and output never appear); a multi-line answer is refused; agent tabs can't reach it |
| **S5a** Phase 1 keys | Keychain key fields, the Anthropic suggester adapter, the usage counter | a key is never logged or written outside the Keychain; portenvd and the helper can't read it; no key means the adapter is skipped; the counter records tokens and latency |
| **S5** Phase 2 adapter (here) and backend (private) | section 6 | each response row in section 6's table; the API down means a command; the adapter conforms to `ClassifyingSuggester` |
| **S6** Dashboard (private) | section 6 | allowlist only |

### Threats

| Threat | What stops it |
| --- | --- |
| Prompt injection through output | Output, files and messages are never sent; the answer is one line that runs only on Return |
| Secrets in history or completions | History stays in the workspace and in memory; entries that look like secrets aren't offered; completions have no network |
| A harmful generator | A fixed, read-only list called by ID; no project code runs; time and size limits |
| The classifier calls a destructive command "language" | It only decides whether a suggester is asked; nothing auto-runs |
| A suggested Portenv action the person didn't mean | Shown in full with ✦ "Runs in Portenv", runs only on Return; portenvd's approvals apply |
| An API key leaks | Keychain only, read by the app at call time; never logged or in either repository; phase 2 keeps model keys server-side |
| The CLI or the editor gains powers | One portenvd call each, the caller fixed at accept, no flags that skip approvals, no owner socket in the workspace |
| The editor types for an agent | Agent tabs don't have it; it sends only as the person |

## Settled text (on acceptance)

- **GUIDELINES §14:** the "custom input editor" item gives way to sections 0 and 1. "A built-in AI assistant" stays, with section 4 noted as not one.
- **GUIDELINES §4** gains the editor and completions.
- **PLAN's decisions** are unchanged: ADR 0006 stands, and ADR 0008 too, since phase 1 has no Portenv backend and phase 2 is a hosted option.

## Open questions (in PLAN.md)

- **The classifier: answered (R-0355).** The built-in rules are the default, and jev (TypeSafe) is the hosted adapter (4a). Still open: the budget and threshold, after measuring.
- **The spec set:** its licence per file, and whether declarative-only specs are enough.
- **Line endings:** proposed not needed (CRLF becomes LF; workspaces are Linux).
- **After a failed suggestion,** a second Return sends the text as typed: confirm.
- **`portenv deploy`:** it refuses without a runbook (proposed). Open: whether staging runs without asking when the owner typed it, and whether runbook target names may be sent (they're not on R-0350's list, so they aren't today).
- **The owner's `msg` and `wake`:** whether the message shows the owner's name, and whether `wake` may wake a busy agent (proposed: no, with "<agent> is busy; it'll see your message when it's next idle.").
- **Phase 2:**
  - the GitHub token's scope for `/v1/suggest` (a separate token with no repository access?);
  - how long the aggregate counts are kept;
  - what the hosted LLM's provider keeps.

## Consequences

- Blocks reach older zsh homes and fish, through one parser.
- With smart input off, the terminal is unchanged. With it on, the plain line is the fallback at every layer.
- The helper gains `RunGenerator` and a per-prompt bracketed-paste field.
- The owner's CLI gains the app's everyday actions, on the same rules as the UI.
- Vendors are adapters. Phase 2's backend replaces phase 1's keys without changing the app's code paths.
