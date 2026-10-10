# 0017. 1.2's spike: tmux control mode, OSC 133, and the waiting-for-input detector

Date: 2026-10-10 · Status: proposed (spike results, for the owner's review)

## Context

PLAN.md 1.2 item 5 time-boxes a spike to three days. It asks three questions:
- Can the box agent map the app's tabs to tmux windows through control mode (`tmux -C`)?
- Can shell integration in the skeleton home emit OSC 133 marks that the agent tracks per tab, with exit codes, for bash and zsh, including inside Claude Code?
- Do SwiftTerm overlays stay aligned to rows when scrolling?

Waiting-for-input detection is launch-critical (the relay loop). If the spike fails, a fallback detector still lands in Phase 1. The owner asked for that fallback to combine all four of Agent readiness principle 5's signals, and for the ADR to show how often each signal alone would be wrong. ADR 0016 point 8 adds that secret prompts are never relayed.

## What the spike found

**1. Control mode: yes.** A control-mode client attached to the box's session gets one line per change:
- `%window-add`, `%window-renamed` and `%window-close`, carrying the same window IDs (`@N`) that #41's tab interface already uses as tab IDs;
- every pane's output (`%output %N …`, octal-escaped).

Tested against a real tmux server, through #41's tab interface (open, rename, close): `TestControlModeSeesTabsAsWindows`. Two things the agent's real client must do:
- **attach before tabs start,** since a prompt printed before the client attaches is never seen;
- **run tmux with a UTF-8 locale.** Without one, tmux prints a format's tab separator as `_`. The agent already sets `LANG=C.UTF-8`; #41's test helper didn't, so it passed or failed depending on the machine, and now sets it.

**2. OSC 133 per tab, with exit codes: yes, for bash and zsh.**
- **bash:** `/usr/share/portenv/shell/osc133.bash`, sourced from `/etc/profile.d`, so every tmux tab, which is a login shell, gets it, existing homes included. It needs bash 4.4 or later for `PS0`; the image has 5.2.
- **zsh:** `osc133.zsh`, sourced by the skeleton's `.zshrc`, so new homes get it.
- **The marks:**
  - A: the prompt starts;
  - B: the person types;
  - C: a command starts;
  - D;exit: it finished, with its exit code.
- **The tracker** follows them per tab, from control mode's output. `TestOSC133PerTabThroughControlMode` covers exit codes 0, 1 and 3, a long command shown as running in its own tab only, and a check that the integration prints nothing visible.
- **Inside Claude Code: only partly.** Claude Code is one long-running command in its tab, so OSC 133 says "running" from start to finish. It can't see that Claude Code is asking something. That's why the combined detector is needed even with integration in place.

**3. SwiftTerm overlay alignment: not answered.** Answering it needs a prototype overlay in the app, which this spike didn't build. It's the remaining step; the reviewer runs the UI test (see "Remaining").

## The waiting-for-input detector

Each tab has four signals (`core/agent/waiting.go`, `waiting_linux.go`):

| Signal | How it's read |
| --- | --- |
| OSC 133 | The tab's shell is running a command, not at its prompt |
| Blocked on the terminal | The foreground process group's live members (the group leader may have exited, as in `seq 500 \| less`): `/proc/<pid>/syscall` is `read` or `readv` on the tab's terminal or `/dev/tty`, or the poll family (`poll`, `select`, `epoll`; Node TUIs such as Claude Code wait in `epoll_pwait`) with the terminal as standard input |
| Quiet | No output from the tab for 1.5 s. The other three signals are read only then: a tab is probed once it has been quiet for 1.5 s, then once a second until output resumes or the shell's prompt returns, because each probe scans `/proc` |
| Screen asks | One of the last 8 non-empty lines reads as a question (`?`, `[y/N]`, `(y/n)`, a trailing `:`, `>>>`), with box-drawing borders stripped. The trailing `:` matches broadly (less's prompt is one); the other three signals filter it |

**The combined verdict:** waiting = a command is running, or the shell state is unknown, and the program is blocked on the terminal, and quiet, and not full-screen (the alternate screen, as in less or vim), and the screen asks.

When the foreground process can't be read, the other signals decide. That happens with setuid programs: sudo, su and passwd run as root, and the agent reads `/proc` as the main user.

**Secret prompts** (ADR 0016 point 8): a prompt is secret when the terminal has echo off with line input on, which is what getpass, sudo and ssh set, or when the last 8 screen lines ask for one:
- **Password-style words count anywhere** in those lines: password, passphrase, PIN, a verification or one-time code, 2FA, OTP.
- **A token, API key, secret, private key, credential or access key counts only when it's asked for:** the word then a colon or question mark ending the line ("Token:", "Private key (PEM):"), or an asking verb shortly before it ("Enter your API token", "Paste your access key").
  - These are whole words, so "tokens" and `GITHUB_TOKEN` don't match.
  - Claude Code shows a token count under its prompt, and code on screen names tokens and secrets. A bare word would make every Claude Code question secret, so it would never be relayed (R-0021).

Checking every line, not only the last, catches a question printed above its input line (an inquirer-style `?` prompt, or a boxed TUI). Raw-mode programs (Claude Code, vim) turn echo off too, but without line input, so they're not mistaken for secret prompts. For a secret prompt, a relay only ever gets "A password is needed; answer it in the terminal.", and an answer sent through a relay is refused.

**An answer is bound to its question.** `Question()` gives each relayed question a random ID, the SHA-256 of its screen lines and its text; a secret prompt gets none. Before an answer is typed, `CheckRelayAnswer` probes the tab again and refuses with "That question has changed; answer it in the terminal." unless the tab is still waiting on the same screen. If the same screen has turned secret, it refuses with the password notice. So a relayed "y" never lands in the next prompt, such as a passphrase that appeared meanwhile (`TestARelayedAnswerNeverReachesTheNextPrompt`, on a real tab). The window between the re-probe and the write is milliseconds; it's accepted.

**The labelled cases.** Each case ran in its own tab on a real tmux server, read through control mode exactly as above. The machine was Linux (Docker Desktop's 6.12 kernel, the same one boxes use), in a Debian bookworm container with bash 5.2, zsh 5.9 and tmux 3.3a, running as an ordinary user. The test is `TestWaitingCases`; CI runs it on Ubuntu 24.04.
- **"Real"** is the actual program.
- **"Stand-in"** is a script that reproduces an agent CLI's terminal behaviour: raw mode, inline, a question above numbered choices, and Node-style epoll or select waits. Stand-ins are used because running the real CLI needs an account and costs money.
- **Expected "waiting"** means a program needs an answer from a person.

| # | Case | Kind | Expected | OSC 133 | Blocked on tty | Quiet | Screen asks | Combined | Secret |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Claude Code asks (permission prompt) | stand-in | waiting | waiting | waiting | waiting | waiting | waiting | — |
| 2 | a second agent CLI asks (Gemini CLI style) | stand-in | waiting | waiting | waiting | waiting | waiting | waiting | — |
| 3 | a script's y/n prompt | real | waiting | waiting | waiting | waiting | waiting | waiting | — |
| 4 | a long build that goes quiet | real | — | waiting | — | waiting | — | — | — |
| 5 | a build that keeps printing | real | — | waiting | — | — | — | — | — |
| 6 | less (a pager) | real | — | waiting | waiting | waiting | waiting | — | — |
| 7 | vim (full-screen editor) | real | — | waiting | waiting | waiting | — | — | — |
| 8 | a REPL (python3) | real | waiting | waiting | waiting | waiting | waiting | waiting | — |
| 9 | a command blocked on the network | real | — | waiting | — | waiting | — | — | — |
| 10 | sudo asks for a password | real | waiting (secret) | waiting | ? (unreadable) | waiting | waiting | waiting | secret |
| 11 | ssh key passphrase | real | waiting (secret) | waiting | waiting | waiting | waiting | waiting | secret |
| 12 | an idle shell prompt | real | — | — | waiting | waiting | — | — | — |
| 13 | a Node CLI asks for an API token (inquirer style, masked) | stand-in | waiting (secret) | waiting | waiting | waiting | waiting | waiting | secret |
| 14 | a boxed TUI asks for an access key (question above the input) | stand-in | waiting (secret) | waiting | waiting | waiting | waiting | waiting | secret |

Cases 13 and 14 are stand-ins for agent CLIs that ask for a credential: an inquirer-style "? Enter your API token:" with a masked line under it (raw mode, waiting in epoll), and a boxed TUI with "Paste your access key:" above its input line. Both are caught as secret by the wording on the lines above the input.

**Each signal alone:**

| Signal alone | False "waiting" | Missed "waiting" |
| --- | --- | --- |
| OSC 133 | 5 of 14 | 0 of 14 |
| Blocked on tty | 3 of 14 | 1 of 14 (sudo: unreadable) |
| Quiet | 5 of 14 | 0 of 14 |
| Screen asks | 1 of 14 (less) | 0 of 14 |
| **Combined** | **0 of 14** | **0 of 14** |

**Why each signal is needed:**
- **OSC 133 alone** calls every running command "waiting".
- **"Blocked" alone** is fooled by full-screen programs and the idle shell, and can't see setuid programs.
- **"Quiet" alone** is fooled by any silent command.
- **"Screen asks" alone** is fooled by less's `:`.

Each false "waiting" is removed by another signal: OSC 133 removes the idle shell; "blocked" removes the quiet build and the network wait; the alternate screen removes less and vim.

**The secret-prompt rule:**
- sudo (real, as a user whose password sudo asks for) and an ssh key's passphrase (real, with a throwaway key) are both detected as secret. A relay gets only the notice, and an answer through a relay is refused.
- The two agent-CLI stand-ins, in raw mode, are correctly **not** secret.
- On CI's runner, which has passwordless sudo, the sudo case uses a getpass stand-in with the same terminal modes, and the table says so.

## Decision

- **Tabs follow tmux through control mode.** The agent runs one control-mode client per box session, attached before tabs start, with `LANG=C.UTF-8`. It replaces #41's polling (`watchTabs`) when 1.2 continues; the tab interface and its IDs don't change.
- **Shell integration ships in the image** as above, and the agent tracks OSC 133 per tab. Command boundaries and exit codes for blocks come from it.
- **Waiting-for-input uses the combined detector everywhere,** not only as a fallback. OSC 133 can't see an agent CLI asking, so no single signal is enough.
- **Phase 1 keeps blocks and the rest, provisionally,** pending question 3:
  - blocks with attribution;
  - the sticky header;
  - the inline answer card;
  - tab badges;
  - the command palette;
  - saved commands.

  If the overlay test fails, the plan moves them to Phase 2, as item 5 says, and this ADR is updated.

## Remaining

- **Question 3, SwiftTerm overlay alignment:** a prototype overlay, then a UI test by the reviewer.
- **The same cases over the SSH forward to a box on a server.** This needs the AWS test server, so it waits for the owner's go-ahead.
- **Real Claude Code and a real second agent CLI** replace the stand-ins later. They need accounts, so not in this spike (the owner, 2026-10-10).
- **Inside a real box,** the agent reads `/proc` and runs `stty` as the main user. The spike ran those as that user directly, but the agent's helper for it isn't built yet.
- **The quiet threshold (1.5 s), the 8 screen lines and the probe interval** are first values, to tune against real agent CLIs.

## Consequences

- The relay loop's "waiting for input" event comes from the combined detector. Its inputs are tmux, `/proc` as the main user, and `stty`, so it needs no ptrace and no new capability.
- Secret prompts never leave the terminal, and the rule is enforced in one place: `RelayQuestion`, `Question` and `CheckRelayAnswer`. A relayed answer reaches only the question it was given for.
- Setuid programs are judged without the "blocked" signal: a slightly weaker verdict, still right in the cases tested.
