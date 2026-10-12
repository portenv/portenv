# SPDX-License-Identifier: Apache-2.0
#
# check-workspace-word.py: what people read says "workspace", never "box"
# (R-0112). Run by make lint; scripts/test-workspace-word.sh tests it.
# Usage: check-workspace-word.py [ROOT]
#
# Scans the string literals of the Mac app's sources and of the Go messages
# portenvd can return to it, and fails on the word box or boxes. Code keeps
# "box" (identifiers, proto, box IDs "box-…", paths, Keychain service names,
# log keys), so these are not flagged:
#   - strings with no space (identifiers, keys, paths, IDs: "box-name",
#     "boxes/", "inspectorOpen.\(box)"), but a lone "Box" or "Boxes" (a menu
#     or column title) is flagged;
#   - "toolbox", and "box-" (the ID format, as in "box- and 12 hex digits");
#   - interpolations, \(box) in Swift, which are values, not words;
#   - Go log and test calls (log keys and messages for developers);
#   - lines marked with a trailing comment "workspace-word: ok (<reason>)".
# The CLI (core/cmd/portenv) is renamed separately and isn't scanned yet.
import pathlib
import re
import sys

ROOT = pathlib.Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else pathlib.Path(__file__).resolve().parent.parent
WORD = re.compile(r"\b[Bb]ox(es)?\b")
OK_MARK = "workspace-word: ok"

SWIFT_DIRS = ["apps/mac/Sources"]
GO_DIRS = ["core/daemon", "core/local", "core/sync", "core/keys", "core/agent", "core/driver"]
# Go calls whose strings people never read.
GO_SKIP = re.compile(r"\b(log|s\.log|e\.log|t|b|tb)\.(Debug|Info|Warn|Error|Fatal|Fatalf|Errorf|Logf|Log|Skip|Skipf|With)\(|\blog\.|slog\.")


def flagged(text):
    """The literal's words, with interpolations dropped; None if fine."""
    text = re.sub(r"\\\((?:[^()]|\([^()]*\))*\)", " ", text)
    if " " not in text.strip() and not re.fullmatch(r"Box(es)?[.…:]*", text.strip()):
        return None
    text = re.sub(r"(?i)toolbox(es)?|\bbox-", "", text)
    return text if WORD.search(text) else None


def swift_literals(src):
    """(line, text) for every string literal, comments skipped."""
    i, line, n = 0, 1, len(src)
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
        if src.startswith('"""', i):
            j = src.find('"""', i + 3)
            j = n if j < 0 else j
            yield line, src[i + 3:j]
            line += src.count("\n", i, j + 3)
            i = j + 3
            continue
        if c == '"':
            j, depth = i + 1, 0
            while j < n:
                if src[j] == "\\" and j + 1 < n and src[j + 1] == "(":
                    depth += 1
                    j += 2
                    continue
                if depth and src[j] == "(":
                    depth += 1
                elif depth and src[j] == ")":
                    depth -= 1
                elif src[j] == "\\":
                    j += 2
                    continue
                elif src[j] == '"' and not depth:
                    break
                elif src[j] == "\n":
                    break
                j += 1
            yield line, src[i + 1:j]
            i = j + 1
            continue
        if c == "\n":
            line += 1
        i += 1


GO_STR = re.compile(r'"((?:[^"\\\n]|\\.)*)"|`([^`]*)`')


def go_literals(src):
    for no, raw in enumerate(src.splitlines(), 1):
        code = raw
        if code.lstrip().startswith("//") or GO_SKIP.search(code):
            continue
        for m in GO_STR.finditer(code):
            yield no, m.group(1) if m.group(1) is not None else m.group(2)


def main():
    bad = []
    for d in SWIFT_DIRS:
        for f in sorted((ROOT / d).rglob("*.swift")):
            lines = f.read_text().splitlines()
            for no, text in swift_literals(f.read_text()):
                if OK_MARK in lines[no - 1]:
                    continue
                if flagged(text):
                    bad.append(f"{f.relative_to(ROOT)}:{no}: \"{text.strip()[:100]}\"")
    for d in GO_DIRS:
        for f in sorted((ROOT / d).rglob("*.go")):
            if f.name.endswith("_test.go") or "testdata" in f.parts or "drivertest" in f.parts:
                continue
            lines = f.read_text().splitlines()
            for no, text in go_literals(f.read_text()):
                if OK_MARK in lines[no - 1]:
                    continue
                if flagged(text):
                    bad.append(f"{f.relative_to(ROOT)}:{no}: \"{text.strip()[:100]}\"")
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
