#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# test-workspace-word.sh: check-workspace-word.py flags "box" in what people
# read and leaves code alone (run by make test). Each case is one planted
# file in a tree of its own.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

# plant NAME swift|go|gotest FLAGGED|CLEAN: the file's content on stdin.
plant() {
	local name=$1 lang=$2 want=$3 root=$tmp/$1 file rc=0 out
	case $lang in
		swift) file=$root/apps/mac/Sources/App/V.swift ;;
		go) file=$root/core/daemon/d.go ;;
		gotest) file=$root/core/daemon/d_test.go ;;
	esac
	mkdir -p "$(dirname "$file")"
	cat >"$file"
	out=$(python3 -I "$here/check-workspace-word.py" "$root" 2>&1) || rc=$?
	if [[ $want == FLAGGED && $rc == 1 ]] || [[ $want == CLEAN && $rc == 0 ]]; then
		echo "ok    $name: $want"
	else
		echo "FAIL  $name: want $want, exit $rc"
		while IFS= read -r l; do echo "      $l"; done <<<"$out"
		fail=1
	fi
}

# What people read: flagged.
plant swift-text swift FLAGGED <<<'let a = Text("Your boxes")'
plant swift-menu-title swift FLAGGED <<<'let b = CommandMenu("Box") {}'
plant swift-lone-lowercase swift FLAGGED <<<'let c = Text("box")'
plant swift-interpolation-before swift FLAGGED <<<'let d = "\(n) boxes"'
plant swift-concatenation swift FLAGGED <<<'let e = "Your " + "boxes"'
plant swift-raw swift FLAGGED <<<'let f = #"Open the "box" again"#'
plant swift-multiline swift FLAGGED <<'EOF'
let g = """
    Nothing can be saved until the box restarts.
    """
EOF
plant swift-quotes-in-interpolation swift FLAGGED <<<'let h = "\(ok ? "a" : "b") box restarts"'
plant swift-all-caps swift FLAGGED <<<'let j = Text("OPEN THE BOX")'
plant swift-all-caps-lone swift FLAGGED <<<'let k = Text("BOXES")'
plant swift-before-unicode-escape swift FLAGGED <<<'let l = Button("New Box\u{2026}") {}'
plant swift-before-newline-escape swift FLAGGED <<<'let m = "Boxes:\nacme-api"'
plant swift-interpolation-touching swift FLAGGED <<<'let n = "\(count)boxes"'
plant go-before-escapes go FLAGGED <<<'var x = "Boxes:\n…"'
plant go-all-caps go FLAGGED <<<'var x = errors.New("NO BOX HERE")'
plant go-error go FLAGGED <<<'var x = errors.New("no key stored for this box")'
plant go-lone-lowercase go FLAGGED <<<'var x = "box"'
plant go-backtick-multiline go FLAGGED <<'EOF'
var help = `Usage:
  opens the box on this Mac`
EOF
plant go-after-a-log-call go FLAGGED <<'EOF'
func f() error { log.Info("box ready", "box", name); return errors.New("the box stopped") }
EOF

# Code: clean.
plant swift-identifiers swift CLEAN <<'EOF'
let d = "box-name"
let e = "inspectorOpen.\(box)"
let f = "\(box) · Saved"
let g = "the toolbox has git"
let h = "A workspace ID is box- and 12 hex digits."
let i = #"raw \#(box) value"#
// A comment about the box is fine.
/* And a block comment about boxes. */
EOF
plant swift-interpolated-identifiers swift CLEAN <<'EOF'
let o = "keychain-\(box)"
let p = "command-\(box)-\(tab)"
let q = "\(box)-storage"
let r = "boxes/\(name).json"
EOF
plant swift-ok-mark swift CLEAN <<<'let i = "Restart Box" // workspace-word: ok (testing the mark)'
plant go-log-arguments go CLEAN <<'EOF'
func f() {
	s.log.Info("box ready", "box", name)
	slog.Warn("saving the box failed",
		"box", name)
	t.Fatalf("the box didn't start")
}
EOF
plant go-ok-mark go CLEAN <<'EOF'
func f() { _ = filepath.Join(dir, "boxes", name) } // workspace-word: ok (a path)
EOF
plant go-comment go CLEAN <<<'// the box and its boxes'
plant go-test-file gotest CLEAN <<<'var y = "a new box in a test"'

# The repository itself.
if python3 -I "$here/check-workspace-word.py" >/dev/null; then
	echo "ok    the repository says workspace where people read it"
else
	echo "FAIL  the repository says box where people read it"
	fail=1
fi
if [[ $fail == 0 ]]; then echo "all checks passed"; else exit 1; fi
