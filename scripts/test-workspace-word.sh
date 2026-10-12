#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# test-workspace-word.sh: check-workspace-word.py flags "box" in what people
# read and leaves code alone (run by make test).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/apps/mac/Sources/App" "$tmp/core/daemon"

cat >"$tmp/apps/mac/Sources/App/V.swift" <<'SWIFT'
let a = Text("Your boxes")
let b = CommandMenu("Box") {}
let c = """
    Nothing can be saved until the box restarts.
    """
// A comment about the box is fine.
let d = "box-name"
let e = "inspectorOpen.\(box)"
let f = "\(box) · Saved"
let g = "the toolbox has git"
let h = "A workspace ID is box- and 12 hex digits."
let i = "Restart Box" // workspace-word: ok (testing the mark)
SWIFT
cat >"$tmp/core/daemon/d.go" <<'GO'
package daemon

var x = errors.New("no key stored for this box")

func f() {
	s.log.Info("box ready", "box", name)
	_ = filepath.Join(dir, "boxes", name)
	_ = "agent channel: the certificate is not this box's" // workspace-word: ok (internal)
}
GO
cat >"$tmp/core/daemon/d_test.go" <<'GO'
package daemon

var y = "a new box in a test"
GO

rc=0
out=$(python3 -I "$here/check-workspace-word.py" "$tmp" 2>&1) || rc=$?
fail=0
t() { if "$@"; then echo "ok    ${*: -1}"; else echo "FAIL  ${*: -1}"; fail=1; fi; }
has() { grep -qF -- "$1" <<<"$out"; }
hasnt() { ! grep -qF -- "$1" <<<"$out"; }
if [[ $rc == 1 ]]; then echo "ok    exits 1 on a finding"; else echo "FAIL  exit $rc"; fail=1; fi
t has 'V.swift:1: "Your boxes"'
t has 'V.swift:2: "Box"'
t has 'V.swift:3:'
t has 'd.go:3: "no key stored for this box"'
for s in box-name inspectorOpen toolbox 'box- and 12' 'Restart Box' 'box ready' '"boxes"' certificate 'a new box in a test' 'comment about'; do
	t hasnt "$s"
done
[[ $(grep -c '^  ' <<<"$out") == 4 ]] || { echo "FAIL  exactly 4 flagged, got:"; echo "$out"; fail=1; }

# And the repository itself is clean.
python3 -I "$here/check-workspace-word.py" >/dev/null || { echo "FAIL  the repository says box where people read it"; fail=1; }
if [[ $fail == 0 ]]; then echo "all checks passed"; else exit 1; fi
