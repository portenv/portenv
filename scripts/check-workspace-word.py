# SPDX-License-Identifier: Apache-2.0
#
# check-workspace-word.py: what people read says "workspace", never "box"
# (R-0112). Run by make lint; scripts/test-workspace-word.sh tests it.
# Usage: check-workspace-word.py [ROOT]
#
# Scans every string literal of the Mac app's sources (plain, multi-line and
# raw #"…"# strings, each piece of a + concatenation) and of the Go messages
# portenvd can return to it ("…" and multi-line `…`), and fails on the word
# box or boxes in any case (BOX too), with escapes read as what they
# write ("Box\u{2026}" is "Box…", "\n" a space) and an interpolation right
# against the word ("\(n)boxes") counted as a word before it.
# Code keeps "box" (identifiers, proto, box IDs "box-…", paths, Keychain
# service names, log keys), so these are not flagged:
#   - identifier-like strings, with no space once each interpolation counts
#     as a word ("box-name", "boxes/", "inspectorOpen.\(box)"); a string that
#     is only the word, "box" or "Boxes", is still flagged (a menu or column
#     title, or a piece of a concatenation);
#   - "toolbox", and "box-" (the ID format, as in "box- and 12 hex digits");
#   - interpolations, \(box) in Swift, which are values, not words;
#   - the arguments of Go log and test calls (log keys and messages for
#     developers); the rest of the line is still read;
#   - literals on a line marked with a trailing comment
#     "workspace-word: ok (<reason>)".
# The CLI (core/cmd/portenv) is renamed separately and isn't scanned yet.
import pathlib
import re
import sys

ROOT = pathlib.Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else pathlib.Path(__file__).resolve().parent.parent
WORD = re.compile(r"\bbox(es)?\b", re.IGNORECASE)
LONE = re.compile(r"\s*box(es)?[.…:,]*\s*", re.IGNORECASE)
OK_MARK = "workspace-word: ok"

SWIFT_DIRS = ["apps/mac/Sources"]
GO_DIRS = ["core/daemon", "core/local", "core/sync", "core/keys", "core/agent", "core/driver"]
# Go calls whose arguments people never read: loggers and test reports.
GO_SKIP_CALL = re.compile(
    r"\b(?:(?:[A-Za-z_][A-Za-z0-9_]*\.)*(?:log|logger|slog)\.(?:Debug|Info|Warn|Error|With|Log|Printf|Println|Print)"
    r"|(?:t|b|tb|f)\.(?:Fatal|Fatalf|Error|Errorf|Log|Logf|Skip|Skipf))\("
)
INTERP = re.compile(r"\\#*\((?:[^()]|\((?:[^()]|\([^()]*\))*\))*\)")


ESCAPES = re.compile(r"\\u\{([0-9A-Fa-f]{1,6})\}|\\u([0-9A-Fa-f]{4})|\\[nrt0]")


def unescape(text):
    """Swift's \\u{…} and Go's \\uXXXX as their characters; \\n, \\t, \\r and
    \\0 as spaces (they separate words as a space does)."""
    def one(m):
        code = m.group(1) or m.group(2)
        return chr(int(code, 16)) if code else " "
    return ESCAPES.sub(one, text)


# An interpolation right against the word ("\\(n)boxes", "\\(n) boxes").
TOUCHING = re.compile(r"\x00\s*box(es)?\b|\bbox(es)?\s*\x00", re.IGNORECASE)


def flagged(text):
    """The literal's text if it says box or boxes to a person; else None."""
    plain = unescape(text)
    as_words = INTERP.sub(" \x00 ", plain)
    if LONE.fullmatch(as_words.replace("\x00", "")):
        return text
    if TOUCHING.search(re.sub(r"(?i)\bbox-", "", INTERP.sub("\x00", plain))):
        return text
    if not re.search(r"\s", INTERP.sub("X", plain).strip()):
        return None
    words = re.sub(r"(?i)toolbox(es)?|\bbox-", "", as_words.replace("\x00", " "))
    return text if WORD.search(words) else None


def swift_literals(src):
    """(line, text) for every string literal, comments skipped."""
    i, line, n = 0, 1, len(src)
    while i < n:
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            line += src.count("\n", i, j)
            i = j
            continue
        m = re.match(r'(#*)("""|")', src[i:i + 64])
        if m and (m.group(1) or src[i] == '"'):
            hashes, quote = m.group(1), m.group(2)
            start = i + len(m.group(0))
            close = quote + hashes
            # Interpolations (\( … ), or \#( … ) in a raw string) may hold
            # strings of their own: skip them by their parentheses.
            opener = "\\" + hashes + "("
            j, depth = start, 0
            while j < n:
                if depth == 0 and src.startswith(close, j):
                    break
                if src.startswith(opener, j):
                    depth += 1
                    j += len(opener)
                    continue
                if depth and src[j] == "(":
                    depth += 1
                elif depth and src[j] == ")":
                    depth -= 1
                elif src[j] == "\\" and not hashes:
                    j += 2
                    continue
                elif quote == '"' and src[j] == "\n":
                    break
                j += 1
            yield line, src[start:j]
            line += src.count("\n", i, min(j + len(close), n))
            i = j + len(close)
            continue
        if src[i] == "\n":
            line += 1
        i += 1


def go_literals(src):
    """(line, text) for every string literal outside comments and outside
    the arguments of log and test calls."""
    i, line, n = 0, 1, len(src)
    skip_depth, depth = None, 0
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            line += src.count("\n", i, j)
            i = j
            continue
        if skip_depth is None:
            m = GO_SKIP_CALL.match(src, i)
            if m and (i == 0 or not (src[i - 1].isalnum() or src[i - 1] in "_.")):
                skip_depth = depth
                depth += 1
                i = m.end()
                continue
        if c in '"`':
            j = i + 1
            while j < n and src[j] != c:
                if c == '"' and src[j] == "\\":
                    j += 1
                elif c == '"' and src[j] == "\n":
                    break
                j += 1
            if skip_depth is None:
                yield line, src[i + 1:j]
            line += src.count("\n", i, j)
            i = j + 1
            continue
        if c == "'":
            j = i + 1
            while j < n and src[j] != "'":
                j += 2 if src[j] == "\\" else 1
            i = j + 1
            continue
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if skip_depth is not None and depth == skip_depth:
                skip_depth = None
        elif c == "\n":
            line += 1
        i += 1


def scan(files, literals):
    bad = []
    for f in files:
        src = f.read_text()
        lines = src.splitlines()
        for no, text in literals(src):
            if no - 1 < len(lines) and OK_MARK in lines[no - 1]:
                continue
            if flagged(text):
                shown = " ".join(text.split())[:100]
                bad.append(f"{f.relative_to(ROOT)}:{no}: \"{shown}\"")
    return bad


def main():
    swift = [f for d in SWIFT_DIRS for f in sorted((ROOT / d).rglob("*.swift"))]
    go = [f for d in GO_DIRS for f in sorted((ROOT / d).rglob("*.go"))
          if not f.name.endswith("_test.go") and "testdata" not in f.parts and "drivertest" not in f.parts]
    bad = scan(swift, swift_literals) + scan(go, go_literals)
    if bad:
        print("People read \"workspace\", not \"box\" (R-0112; docs/design/GUIDELINES.md §2):", file=sys.stderr)
        for b in bad:
            print("  " + b, file=sys.stderr)
        print("Rename the word, or, if no person ever reads the string, end the line with "
              "// workspace-word: ok (<reason>).", file=sys.stderr)
        return 1
    print("workspace word: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
