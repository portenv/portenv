# 0031. Voice input: push-to-talk into the input line

Date: 2026-10-19 · Status: proposed, for tester build 2, after ADR 0027's S1 (the owner's R-0382; timing R-0383). Tester build 1 relies on macOS Dictation where the basics audit shows it works (section 8).

## Context

The owner wants to talk to the terminal: say "wake the writer" or "run the tests", see the text in the input, and press Return himself (R-0382).

Most of the path exists or is proposed:
- **ADR 0027 (#228, proposed)** gives the input line at a shell prompt (`InputLine`, S1), and English to a command behind it: the built-in rules, then a classifier, then a suggester. Every layer falls through to the plain line (R-0349, R-0354), and nothing runs without Return.
- **Its adapters** sit behind small protocols (`Completer`, `Classifier`, `Suggester`), one vendor per adapter file, chosen in Settings (R-0354). Voice follows the same pattern.
- **A paste** goes through `TabTerminal` as the person, attributed by portenvd (ADR 0020 decision 4), to tmux's `paste-buffer -p`, bracketed only if the program asked (#216). Claude Code, a full-screen program, asks.
- **macOS Dictation** (press fn twice) types into whatever text view has focus. It may already work in a terminal tab; the basics audit checks.

## Decision

### 1. Push-to-talk

- **Hold the talk key, speak, release.** The transcript lands in the input (section 3). A click on the microphone in the input starts listening; a second click stops it. Edit › Start Talking does the same and shows the key.
- **Nothing runs on its own.** The transcript never carries Return. Return stays the person's, so a misheard sentence is only text on the line.
- **The talk key** is set in Settings › Voice. The proposed default is **right Option, held alone**:
  - listening starts only after 250 ms held, so a quick press does nothing;
  - pressing another key while holding it cancels listening and passes the chord through as usual (Option as Meta in the shell keeps working);
  - fn and the globe key aren't offered, since macOS uses them for Dictation and input sources.
- **Only in Portenv's own window.** The key is read by the app's own key handling while a Portenv window is in front, never by a global hotkey, so no Input Monitoring or Accessibility permission is needed.
- **Stops by itself:** at release, at the second click, at 60 s (what was heard is kept), when the window loses focus, when the tab closes, or on Escape. Focus loss, a closed tab and Escape discard what was heard.
- **Off until turned on.** In tester build 2, voice input sits under Settings › Experimental › "Voice input", off by default, on its own toggle (it doesn't need smart input). With it off, nothing listens and there's no microphone button and no menu item.

### 2. Permissions, asked on first use

- **Only on the first press** of the talk key or the first click on the microphone, never at launch or in first run.
- **Two system prompts,** for the default adapter: the microphone, then speech recognition (Apple's recognizer needs both, even on the device). Other adapters ask only for the microphone.
- **The usage strings** (drafts, `Info.plist`):
  - `NSMicrophoneUsageDescription`: "Portenv listens only while you hold the talk key or click the microphone, and turns what you say into text in your terminal. Recordings are never saved."
  - `NSSpeechRecognitionUsageDescription`: "Portenv uses your Mac's own speech recognition to turn what you say into text. It runs on this Mac."
- **The hardened runtime** needs the `com.apple.security.device.audio-input` entitlement. It's added to the release and lane builds, with a test that the built app carries it and both usage strings.
- **Refused:** the microphone shows "Microphone access is off for Portenv." with Open System Settings (Privacy & Security › Microphone). Portenv doesn't ask again; typing is unaffected.

### 3. Where the transcript goes

At release, the transcript goes to the tab where listening started, through the same path the person's own keys would take there:
1. **Smart input on and the editor showing** (a shell prompt in the person's tab, ADR 0027 section 1): it's inserted at the cursor, replacing any selection, as one undo step (⌘Z takes it out). Then ADR 0027's path is unchanged: Return goes through S3's parser, the built-in rules, the classifier and the suggester, and every R-0349 fall-through holds. "wake the writer" becomes the ✦ suggestion `portenv wake writer`, and a second Return runs it.
2. **Anywhere else** (smart input off, a running program, an agent's Claude Code tab or any other full-screen program): it's sent as one paste through #216's path, as the person, with no CR.
   - **A full-screen program that asked for bracketed paste** (Claude Code does) gets `ESC[200~…ESC[201~`. The text sits in its input box until the person presses Return.
   - **A program that didn't ask** gets the plain characters, still with no line ending.
   - **In an agent's tab** the paste is the owner's, attributed to "You" by portenvd, exactly as if he'd typed it. Voice never types for an agent, and agents never hear or see the audio or the partial text.
3. **Cleaned first, in both cases:** line breaks become spaces, and control characters (ESC included) are removed, so nothing in a transcript can submit a line or end a bracketed paste early. At most 2,000 characters.
4. **The tab went away, or its prompt changed while listening:** the transcript is discarded, with "Nothing was inserted: the tab closed." (or "…the prompt changed.") at the microphone for a few seconds. It's never kept for later.

**The InputLine contract gains one call** (ADR 0027 section 7; agreed with S1 before it changes, as that ADR asks):

```swift
@MainActor public protocol InputLine: AnyObject {
    // … ADR 0027's members, unchanged …
    /// The person's own input from another source (voice). Inserted at the
    /// selection as one undo step and marked `.dictated` in the line's origin.
    /// Returns false, inserting nothing, when the line isn't active.
    @discardableResult func insertFromPerson(_ text: String, source: String) -> Bool
}
```

Only S1 changes the text, as before; this is the person's input reaching S1 by another route, like a paste.

**Spoken commands and the rules (proposed).** People rarely speak shell syntax. A line that is entirely dictated and holds no shell syntax counts as a strong sign of English for ADR 0027's built-in rules, unless its first word is a known program ("git status" stays a shell line). The flag lives only on the Mac and is never sent. This is a change to ADR 0027's rules, so it's an open question for the owner.

### 4. The Transcriber interface (R-0354's pattern)

App code talks only to these. Each engine is one adapter file; the choice is in Settings › Voice.

```swift
public struct AudioChunk: Sendable {           // 16 kHz mono Float32, from VoiceCapture only
    public let samples: [Float]; public let hostTime: UInt64
}
public struct TranscriptionOptions: Sendable {
    public let locale: Locale                  // the system's language unless set in Settings
    public let hints: [String]                 // team agent IDs and names, `portenv` verbs (R-0350's list)
}
public enum TranscriptUpdate: Sendable { case partial(String), final(String, confidence: Double?) }
public enum TranscriberAvailability: Sendable {
    case ready, needsPermission, unavailable(reason: String)  // reason is shown, never logged with audio
}
public protocol Transcriber: Sendable {
    var id: String { get }                     // "apple-on-device", "whisper-cpp", "portenv-cloud"
    var audioLeavesThisMac: Bool { get }       // false for every phase 1 adapter
    func availability(_ o: TranscriptionOptions) async -> TranscriberAvailability
    /// Reads `audio` until it ends, yielding partials, then one final. Throws or
    /// ends without a final on any failure; the runner treats both as no text.
    func transcribe(_ audio: AsyncStream<AudioChunk>, _ o: TranscriptionOptions)
        -> AsyncThrowingStream<TranscriptUpdate, Error>
}
```

- **One capture, one indicator.** `VoiceCapture` alone opens the microphone (`AVAudioEngine`) and feeds chunks to the adapter. The indicator is tied to it, so an adapter can't listen without the indicator showing. Chunks live only in memory and are dropped as they're consumed.
- **One voice runner** owns the key, the limits, the timeouts, the checks on answers, the circuit breaker and the usage counter, as ADR 0027's fall-through runner does. Adapters own none of these.
- **The limits:**
  - a final within **2 s of release**, or there's no text;
  - 60 s of listening at most;
  - silence the whole time (the level stays under a floor) means no text, without asking the adapter.
- **Garbage is no text:** empty or punctuation-only results; a confidence under the adapter's threshold, when it gives one; known silence phrases an engine invents (whisper's "Thank you for watching." and the like, a list per adapter); anything over 2,000 characters.
- **The circuit breaker** is ADR 0027's: three failures in a row skip the adapter for 60 s, doubling up to 15 min, and a success resets it. A skipped adapter means the microphone shows "Voice input isn't available right now." and typing goes on.
- **Every failure falls through to no text.** No permission, an adapter that isn't ready, an error, a timeout or garbage: nothing is inserted, the microphone says why in one plain line for a few seconds, and typing never waits. Keys typed while listening (in click mode) go where they always go.

**The adapters:**

| Adapter | When | Audio | Notes |
| --- | --- | --- | --- |
| **Apple on device** (default) | tester build 2 | never leaves the Mac | the Speech framework, on-device recognition required |
| **whisper.cpp** | later, phase 1 | never leaves the Mac | MIT; a model the person downloads in Settings, pinned by SHA-256, run in a separate process with no network |
| **Portenv cloud** | phase 2, after the owner's go | goes to Portenv's own API only | opt-in with its own privacy note (section 7) |

**How "on device" is enforced** for the Apple adapter:
- it builds an `SFSpeechRecognizer` for the locale and refuses to start (`unavailable`) unless `supportsOnDeviceRecognition` is true;
- every `SFSpeechAudioBufferRecognitionRequest` it makes has `requiresOnDeviceRecognition = true`, set in one place;
- it never retries without it: a locale with no on-device model is "Speech for <language> isn't available on this Mac." and no text, never Apple's server;
- `audioLeavesThisMac` is false, and a test fails if any phase 1 adapter says otherwise or builds a request without the flag.

macOS 26's newer `SpeechAnalyzer` runs only on the device by design; a second Apple adapter on it is an open question, not a change to this rule.

### 5. While listening

- **A visible indicator:** the microphone in the input (or, without the editor, a small "Listening" badge at the tab's prompt row) turns red with a live level, and the partial transcript shows tinted where the text will go. With Reduce Motion, the level is a static symbol. macOS also shows its own microphone light in the menu bar.
- **Partial text** stays on screen only. It's never sent to a tab, an agent or any adapter but the transcriber.
- **Nothing is kept:** no audio is written to disk (not even a temporary file), and no transcript or partial text is logged. Logs and the local usage counter hold only counts: recordings, seconds listened, failures by kind, fallbacks, and p50 and p95 time from release to text.

### 6. Accessibility

- **The microphone** is a button: "Start talking" or "Stop talking", with the talk key in its help. Edit › Start Talking is reachable with Full Keyboard Access.
- **VoiceOver announcements** don't move focus: "Listening" at the start; then "Inserted: <text>" or the plain line for no text ("Nothing heard.", "Voice input isn't available right now."). While listening, Portenv announces nothing else, so VoiceOver's own speech isn't transcribed. Whether to pause other announcements during listening is an open question.
- **Colour isn't the only signal:** the indicator has the word "Listening" next to it.

### 7. Phase 2: the Portenv cloud adapter (after the owner's go)

- **One route out:** audio goes only to Portenv's own API (the private `portenv/cloud` repository), with the same GitHub sign-in as ADR 0027 section 6. Never straight to a vendor from the Mac.
- **Opt-in, with its own note** before it's used: what leaves the Mac (the audio of each press and the hints), what reaches a speech provider, and that neither audio nor text is kept, with only aggregate counts.
- **Falls through** like everything else: down, refused or slow means no text. It never replaces the on-device adapter as the default.
- **Whether it shares one call with `/v1/suggest`** is an open question.

### 8. macOS Dictation, and tester build 1

- **Dictation is the system's;** Portenv doesn't change it. In the smart input editor (an `NSTextView`) it works as in any text field. In the terminal view it works only if the terminal accepts the system's text input; the basics audit checks a shell tab and a Claude Code tab.
- **Tester build 1** relies on Dictation if the audit shows it works in both, with a one-line note in `docs/testers.md` added with the audit's result. Push-to-talk comes in tester build 2.
- **How push-to-talk differs:**
  - it's held, not toggled, and only in Portenv's window;
  - it never carries Return. Dictation's spoken "new line" may reach a shell as Return; the audit checks that too;
  - it goes through ADR 0027's rules, classifier and suggester, and arrives in a full-screen program as one bracketed paste;
  - its default engine is required to stay on the Mac. Dictation follows the system's own settings.
- **They don't conflict.** Different keys (fn is never offered as the talk key), and both end in the person's input path. Push-to-talk doesn't start while Portenv's window isn't in front.

### 9. Slices

Tests first, one PR per slice, under the 200-line guard. Each adapter has a failure test, a timeout test and a garbage test, each proving no text and typing that goes on (R-0354).

| Slice | Builds | First tests |
| --- | --- | --- |
| **V0** Contracts | `Transcriber`, `VoiceCapture`'s protocol, the voice runner, `ScriptedTranscriber`, the `InputLine` call (with S1) | the runner: an adapter that throws, hangs past 2 s, or returns empty, punctuation, a silence phrase or 2,001 characters inserts nothing; the breaker opens after three and cools down; silence never calls the adapter |
| **V1** Push-to-talk | the key, the click, Edit › Start Talking, the indicator, the permissions | a press under 250 ms or a chord doesn't listen; focus loss, a closed tab and Escape discard; 60 s stops and keeps; permission is asked on the first press, never at launch; refused means typing works and no second ask; the toggle off means no microphone use at all; no file is written under the app's folders or the temporary folder during a session |
| **V2** Apple adapter | section 4's adapter | every request has `requiresOnDeviceRecognition`; no on-device support means `unavailable` and no request; failure, timeout and garbage fall through; a recorded clip gives its expected text on the CI Mac (skipped without the model) |
| **V3** Delivery | section 3 | with the editor: one undo step, then ADR 0027's path unchanged (with smart input's adapters off, the line is a command); without it: a transcript holding CR, LF, `ESC[201~` and other controls reaches bash, zsh and a bracketed-paste stand-in for Claude Code as one paste with no line ending and no early paste end; an agent's tab records the owner as author; a changed prompt or closed tab discards |
| **V4** Settings, accessibility, counter | Settings › Voice, the announcements, the usage counter | VoiceOver labels and announcements; Reduce Motion; the counter holds no text (a planted marker sentence never appears in logs or the counter's file) |
| **V5** whisper.cpp | later | the model's hash is checked; the process has no network; failure, timeout and garbage (silence phrases) fall through |
| **V6** Portenv cloud | phase 2 | each response kind falls through; nothing is sent without the opt-in; the adapter says `audioLeavesThisMac` |

### Threats

| Threat | What stops it |
| --- | --- |
| Listening by accident | Off until turned on; a held key (250 ms), or a click; only in Portenv's window; stops at focus loss and at 60 s; a red indicator with a word, and macOS's own light; nothing runs without Return |
| Audio leaving the Mac | The default adapter requires on-device recognition and refuses locales without it; one capture owner; the cloud adapter only in phase 2, opt-in, to Portenv's own API |
| Audio or transcripts kept | Memory only, never written; logs and the counter hold counts only; a test plants a sentence and checks it's nowhere on disk |
| A secret spoken aloud | It lands visibly in the line and goes nowhere until Return. On Return, ADR 0027's redaction (its secret words and token shapes) runs before any classifier or suggester call, as for typed text; the rules send known-program lines to no service at all |
| A transcript that submits or escapes | Line breaks and control characters removed; no CR ever; bracketed paste only when the program asked |
| Typing into an agent's tab | Only as the owner, attributed to him by portenvd, as his keys would be; agents never get the audio or partials |
| A mishearing that becomes an action | It's text first; a `portenv` suggestion is shown with ✦ "Runs in Portenv" and runs only on Return, under portenvd's approvals |

## Settled text (on acceptance)

- **GUIDELINES §4** gains push-to-talk (sections 1, 3 and 5), and **§12** its announcements (section 6).
- **PLAN's decisions** are unchanged: phase 1 has no Portenv backend, and the phase 2 adapter is a hosted option (ADR 0008).

## Open questions (in PLAN.md)

- The default talk key: right Option held alone, or a chord such as ⌃⌥Space.
- A dictated line as a strong sign of English for ADR 0027's rules (section 3).
- A second Apple adapter on macOS 26's `SpeechAnalyzer`, and which one is the default.
- Languages: the system's only, or a choice in Settings; and whether the hints help enough to keep.
- Pausing other VoiceOver announcements while listening.
- Where the whisper.cpp model is downloaded from, and its size limit.
- Phase 2: one call with `/v1/suggest` or a separate `/v1/transcribe`, and what the speech provider keeps.

## Consequences

- Talking to the terminal costs no network and stores nothing by default, and a failure is only "no text".
- The `InputLine` contract gains one call; ADR 0027's other contracts are unchanged.
- The app gains the microphone and speech-recognition usage strings and the audio-input entitlement.
- Engines are adapters, so whisper.cpp and the phase 2 cloud adapter change no code path.
