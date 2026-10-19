# 0027. Terminal input: an editor at the prompt, shell-integration blocks, local completions, and experimental natural language

Date: 2026-10-19 · Status: proposed (post-tester). From the owner's decision R-0348 ("Warp-style terminal input") and addenda R-0349 (the default is "it's a command"), R-0350 (suggestions can be Portenv's own commands) and R-0351 (fix the interfaces so four streams build in parallel). Nothing here is built before the tester build ships.

## Context

Most of what this needs exists:
- **OSC 133 hooks** (ADR 0017): bash from `/etc/profile.d` (every home), zsh from the skeleton's `.zshrc` (new homes only), emitting A, B, C and D;exit.
- **The helper's tracker** (`core/agent/osc133.go`) feeds ADR 0017's waiting detector and ADR 0020's mark replay.
- **The app** follows the same marks (`OverlayTracker`, which records each prompt's B column), builds `CommandBlock`s and draws blocks (`BlockCards`).
- **Input** goes through `TabTerminal` as the person, attributed by portenvd (ADR 0020 decision 4); since #216, a paste goes to tmux's `paste-buffer -p`, bracketed only if the program asked.
- **The CLI** has the owner's commands on the Mac (`status`, `attach`, `app …`) and an agent's in a workspace through its grant socket (`msg`, `tab new`, `team status`, `inbox`, …).

GUIDELINES §14 says "a custom input editor" and "a built-in AI assistant" are not done. This ADR proposes changing the first and keeping the second (see "Settled text").

## Decision

### 1. The input editor (stream S1)

At a shell prompt, the input line is an in-app editor (an `NSTextView` over the prompt row from B's column): multi-line, mouse selection, undo and redo, macOS text editing, IME, VoiceOver ("Command input, <tab>").
- **Keys:** Return submits; ⇧Return and ⌥Return insert a new line; ⌃C clears (undoable); ⌃D on an empty line goes to the shell. ↑ on the first line, ⌃R and any key the editor doesn't handle **hand over**: the text goes to the shell's own line as a one-line paste, then the key, and the editor is off until the next prompt.
- **On only when all hold:** the tab's last mark is A or B in the current epoch; the alternate screen is off (vim, less, an ssh session's full-screen program, agent TUIs such as Claude Code); the tab's own shell is the foreground process; marks aren't "unknown" (a `skipped` reset, integration off); the tab is the person's; its toggle (View › Input Editor, default in Settings › Terminal) is on. Otherwise: the plain terminal, exactly as today.
- **Sending:** only at the same prompt it opened on, with no other author since. One line: its bytes, then CR. Several lines: one paste through #216's path, then CR, and only when the helper saw `ESC[?2004h` after this prompt's A; otherwise the text stays, with "This shell isn't taking multi-line input. Join the lines, or turn off the editor for this tab." CRLF becomes LF.
- **Agents:** agent tabs never have the editor. It sends only as the person, with portenvd's author, so it never types for an agent. If another author types at the prompt (stand-in mode, a CLI attach, a routine), it hides until the next prompt and never sends over their text. Its text, drafts and suggestions are app-only; agents never see them.

### 2. Shell-integration blocks (S1)

Reused, with no second parser: the hooks, the helper's tracker and replay, `OverlayTracker`, `CommandBlock`s, the block UI, and ADR 0017's detector. New:
- **zsh for every home** from `/etc/zsh/zshrc`; **fish** from `/etc/fish/conf.d/portenv-osc133.fish` (fish events; fish isn't in the image, so it's for people who add it). Each sets a guard variable, so sourcing twice does nothing, and skips marks the prompt already prints.
- **Installed in system files, never in the person's dotfiles,** so dotfiles can't overwrite them except by replacing `PROMPT_COMMAND` or PS0. Then the tab has no marks, blocks and the editor are off, and Settings › Terminal offers Repair: one guarded line at the end of `~/.bashrc` or `~/.zshrc`, added once, on the owner's click.
- **Opt-out:** `/home/work/.portenv/shell-integration` holding `off` (Settings › Terminal › "Mark commands for blocks"). ADR 0017's detector then runs on its other three signals, as for "unknown".
- **Per prompt, the helper reports whether bracketed paste was turned on** (section 1's multi-line rule); the bash hook turns readline's on.

### 3. Local completions (S2)

Specs, generators, history, project scripts and team names; **no network** on this path.
- **Specs:** the Fig-style open-source spec set (`withfig/autocomplete`, MIT by its repository's licence metadata, 2026-10-19; compatible with Apache-2.0 with its notice), vendored at a pinned commit, converted at build time to declarative JSON, updated only with the app. Parts that need JavaScript are dropped; no third-party code runs.
- **Generators:** a fixed, reviewed list shipped in both the app and the helper. The app asks by ID (`RunGenerator(tab, id, prefix)`, relayed by portenvd); no command text crosses. They're read-only and never run project-controlled code: scripts come from parsing `package.json` and the Makefile, and git runs with fsmonitor and hooks off. They run as the tab's user in the shell's directory, with no stdin or terminal, and are killed at 500 ms or 64 KB.
- **History** comes from the shell's history file and this session's blocks, ranked in memory. It's never written to the Mac's disk and never sent. An entry matching the secret patterns (section 4) isn't offered.
- **Portenv's commands and team names** come from the command list (section 6) and the team files.

### 4. Natural language to a command (S4, experimental, off by default)

**The governing rule (R-0349): it's a command unless proven otherwise.** The line behaves like a normal shell line whenever the feature is off (the default) or not built (today), the service is known unreachable or not signed in, the classifier isn't confident, or anything fails: no delay on Return, no popup, no message.

**The order on Return** (no network before step 4):
1. Feature off, or the editor not showing: send at once.
2. Service marked unavailable by a background check (never made on Return): send at once.
3. The local classifier, in-process, 20 ms budget: not confident, over budget or an error, send at once.
4. Confident "natural language": the line isn't sent. The editor dims it in place and calls the service, with a 4 s timeout.
   - **An answer:** it replaces the text as one undo step, marked "Suggested". Nothing runs until Return.
   - **A timeout, an error, or an unusable answer:** the text is shown as typed, with no message, and the next Return sends it without asking again. A line sent seconds later on its own would be a surprise, so nothing ever auto-runs.

**The classifier** is the owner's choice, jev (R-0349), behind a pluggable interface: `CommandClassifier.classify(_ line: String) -> (verdict, confidence)`, local and offline, with its threshold leaning to "command". The exact upstream project and its licence are an open question.

**The model call** is a hosted feature (ADR 0008): an ordinary choice in Settings once it exists, with sign-in, never blocking anything, and the own-route job runs with it unreachable. Until then, S4 builds against a client stub behind the flag.

**What's sent (R-0350), and only this:**
- **By default:** the sentence, the shell's name, the command list (section 6), each team agent's ID, name and role, and a request ID.
- **With an opt-in, off by default** ("Send project script names"): the project's script names (`package.json` script keys and Makefile targets, parsed, never run; at most 100, 64 characters each).
- **Never:** file contents, messages, decisions' text, output, the screen, history, environment, secrets, file or directory names, or runbooks.
- **Redaction first:** every field passes ADR 0017's secret words plus token shapes, each match becoming `<secret>`. A sentence over 500 characters isn't sent.
- Settings shows what each request sent.

**The answer** is one line of at most 2 KB, cleaned like a paste. It can be a shell command or a Portenv command, for example "wake the writer" → `portenv wake writer`, or "deploy to staging" → `portenv deploy staging`. It only fills the editor.

**Agents never use this path,** and it never acts on its own. It fills the person's editor; it isn't an agent of Portenv's own (ADR 0006).

### 5. The CLI surface (S3)

Owner commands on the Mac, through portenvd, with the names consistent with today's:

| Command | Today |
| --- | --- |
| `portenv msg AGENT TEXT [--thread ID]` | new for the owner (agents have it); portenvd's `SendMessage` exists |
| `portenv wake AGENT` | new; the helper's `WakeAgent` exists, portenvd gains an owner call |
| `portenv tab new --name NAME [-- COMMAND…]` | new for the owner (agents have it); `NewTab` exists |
| `portenv team start [AGENT…]`, `portenv team stop [AGENT…]` | new; `StartAgent` and `StopAgent` exist |
| `portenv deploy TARGET` | new, once ADR 0026 (#224) is accepted. With no runbook it refuses: "This project has no deploy runbook, so Portenv won't guess how to deploy. Add DEPLOY.md (see `portenv guide`), then try again." |
| `portenv decision list`, `portenv decision answer ID CHOICE` | new; `AnswerDecision` exists (`answer` stays PLAN's short alias) |
| `portenv status` | exists |

**No extra powers: portenvd decides.**
- Each command is one portenvd call, the one the UI makes. The caller is fixed when the connection is accepted: the owner on the owner-only socket, an agent by its grant.
- Approvals appear only in the app; `deploy production` waits on the same sheet as the button.
- No flag skips an approval. The workspace's shell (`work`) has no owner socket, so these commands there get the refusal they get today.
- **From the editor,** a line whose first word is `portenv` and whose verb is in the list is marked "Runs in Portenv". Return runs it as the app's action through portenvd, never in the shell; `command portenv …` reaches the shell's.

### 6. The contracts

**InputLine API** (S1 owns and implements it; S2 and S4 consume it; PortenvKit):

```swift
public struct PromptID: Hashable, Sendable { public let line: Int; public let epoch: Int }
public enum ShellKind: String, Sendable { case bash, zsh, fish, unknown }

public struct InputSuggestion: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        case completion(replacing: Range<Int>, with: String)  // UTF-16 offsets into `text` at `revision`
        case wholeLine(String)                                // S4's answer; "Suggested"
    }
    public let kind: Kind
    public let label: String          // what the list shows
    public let detail: String?        // description, shortcut, "Runs in Portenv"
}

public enum InputLineEvent: Equatable, Sendable {
    case activated(PromptID, ShellKind)          // the editor owns the line
    case edited(revision: Int)                   // text or selection changed
    case accepted(offer: UUID, index: Int)       // the person took a suggestion
    case dismissed(offer: UUID)                  // Escape, typing past it, or stale
    case submitted(PromptID)                     // handed to the pane or to Portenv
    case deactivated                             // hand-over, command started, tab hidden
}

public enum SubmitDecision: Sendable { case send, hold, runInPortenv }

@MainActor public protocol SubmitInterceptor: AnyObject {
    /// Called on Return, in order (S3's parser, then S4). Must answer within
    /// 20 ms and never touch the network; a slow, throwing or missing answer
    /// counts as .send. .hold keeps the text; the interceptor may then offer
    /// a .wholeLine suggestion.
    func decide(_ text: String, prompt: PromptID) -> SubmitDecision
}

@MainActor public protocol InputLine: AnyObject {
    var tab: String { get }                  // "@N"
    var prompt: PromptID? { get }            // nil when inactive
    var text: String { get }                 // read-only outside S1
    var selection: Range<Int> { get }        // UTF-16; empty range = cursor
    var revision: Int { get }                // bumps on every edit
    var events: AsyncStream<InputLineEvent> { get }  // multiple consumers
    /// Offer suggestions for `revision`. Dropped as stale if the text has
    /// moved on. A newer offer from the same source replaces the older one.
    @discardableResult
    func offer(_ items: [InputSuggestion], at revision: Int, source: String) -> UUID
    func withdraw(_ offer: UUID)
    func addInterceptor(_ i: SubmitInterceptor)
}
```

- **Ownership:** S1 alone changes the text. A suggestion changes it only when the person accepts it (Tab, Return in the list, or a click), as one undo step, and nothing is ever submitted by a consumer.
- **Threading:** everything is on the main actor. Consumers do slow work (generators, the service) off it, then `offer` on main.
- **Submit:** S1 runs the interceptors, then sends to the pane through `TabTerminalSource.send` (section 1's one-line or paste rules), or calls S3's runner for `.runInPortenv`.
- **Tests:** `ScriptedInputLine` is the mock S2 and S4 build against until S1 merges.

**The command list** (S3 owns it; S2 and S4 read it). It's generated from the CLI's command registry, so it can't drift, and served by `portenv help --json`. The same file is bundled in the app at build time:

```json
{
  "schema": "portenv.commands/1",
  "version": "1.4.0",
  "commands": [
    {
      "path": ["deploy"],
      "aliases": [],
      "summary": "Run the project's deploy runbook for a target.",
      "caller": ["owner", "agent"],
      "approval": "depends",
      "approval_note": "Production always asks in the app.",
      "args": [
        {"name": "target", "type": "runbook_target", "required": true, "repeated": false}
      ],
      "flags": [],
      "since": "1.4.0"
    }
  ]
}
```

- **`type`** is one of `string`, `text`, `int`, `bool`, `path`, `agent_id`, `tab_id`, `decision_id`, `choice`, `runbook_target`, `command` (everything after `--`).
- **`approval`** is `never`, `always` or `depends`. It's for display only, and portenvd decides.
- **`schema`** changes only on an incompatible change; readers refuse an unknown major version and fall back to no Portenv suggestions.
- **Tests:** a checked-in fixture is S2's and S4's mock until S3 merges, and a test fails if the registry and the bundled file differ.

### 7. Streams

After the tester build, the four streams run in parallel on the contracts above. Each mocks the other side until it merges, writes its tests first, ships one PR per slice under the normal 200-line guard, and touches only its own files plus the contract file (which changes only by agreement of the streams that use it).

| Stream | Builds | Exposes / consumes | First tests |
| --- | --- | --- | --- |
| **S1** Input editor and blocks | the editor, sections 1–2, pass-through in full-screen programs | exposes InputLine | a heredoc reaches bash, zsh and fish intact; no editor in vim, less, ssh or an agent TUI; an agent at the prompt hides it; no send at a changed prompt; system hooks reach existing homes; the opt-out and Repair; the latency gate (ADR 0020) holds |
| **S2** Completions | section 3 | consumes InputLine and the command list | works with the network blocked; an unknown generator is refused; a hostile repository config runs nothing; generators killed at 500 ms; a secret in history isn't offered |
| **S3** CLI | section 5 and the command list | publishes the command list; S1's `.runInPortenv` runner | each command on an agent's socket gets only its grant; `deploy production` waits on the app; no runbook refuses; `work` gets the refusal; the registry matches the bundled file |
| **S4** English → command | section 4, a client stub behind the flag | consumes InputLine and the command list | with the feature off, unreachable or hanging, a command's Return makes no network call and adds no delay; a timeout gives the text back and the second Return sends it; request bodies hold only the listed fields (markers planted in files, messages and output never appear); a multi-line answer is refused; agent tabs can't reach it |

### Threats

| Threat | What stops it |
| --- | --- |
| Prompt injection through output | Output, files and messages are never sent; the answer is one line that runs only on Return |
| Secrets in history or completions | History stays in the workspace and memory; secret-shaped entries aren't offered; completions have no network |
| A harmful generator | A fixed read-only list, called by ID; no project code run; time and size limits |
| The classifier calls a destructive command "language" | It only decides whether the model is asked; nothing auto-runs |
| A suggested Portenv action the person didn't mean | Shown in full, "Runs in Portenv", runs on Return; portenvd's approvals apply |
| The CLI or editor gains powers | One portenvd call each, the caller fixed at accept, no skip flags, no owner socket in the workspace |
| The editor types for an agent | Agent tabs have none; it sends only as the person |

## Settled text (on acceptance)

GUIDELINES §14's "custom input editor" item is replaced by section 1's rules, and "a built-in AI assistant" stays, with section 4 noted as not one. §4 gains the editor and completions. PLAN's decisions (ADR 0006, ADR 0008) are unchanged.

## Open questions (in PLAN.md)

- **The classifier:** jev (R-0349). Which upstream project, its licence, size and bundling.
- **The spec set:** its licence per file, and whether declarative-only specs are enough.
- **The editor's default** for new shell tabs (proposed: on).
- **Line endings:** proposed not needed (CRLF becomes LF; workspaces are Linux).
- **After a failed suggestion,** a second Return sends the text as typed: confirm.
- **`portenv deploy`:** refuses without a runbook (proposed); whether staging runs without asking when the owner typed it; whether runbook target names may be sent to the model (not on R-0350's list, so not today).
- **The owner's `msg` and `wake`:** is a message shown as from the owner by name; and may `wake` wake a busy agent (proposed: no, with "<agent> is busy; it'll see your message when it's next idle.").
- **The editor running `portenv …` lines through portenvd** instead of the shell: confirm.

## Consequences

- Blocks reach older zsh homes and fish, with one parser.
- The plain terminal stays the fallback whenever any condition fails.
- The helper gains `RunGenerator` and a per-prompt bracketed-paste field.
- The owner's CLI gains the app's everyday actions on the same rules as the UI.
- Natural language waits on the hosted service, and never changes what Return does for a command.
