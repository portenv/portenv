#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/check-ssh-keygen.sh on planted files: every ssh-keygen run
# without -f, or that rewrites a file (-H, -R, -A) under the person's real
# ~/.ssh, fails; a run on a file of the test's own, and plain mentions, pass.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
check() { scripts/check-ssh-keygen.sh "$dir" >/dev/null 2>&1; }

cat >"$dir/ok_test.go" <<'GO'
	out, err := exec.Command("ssh-keygen", "-H", "-f", f).CombinedOutput()
	cmd, err := bounded.Command(ctx, 10*time.Second, "ssh-keygen", "-F", n, "-f", f)
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path)
	t.Fatalf("ssh-keygen: %v: %s", err, out)
	{name: "passphrase", cmd: "ssh-keygen -y -f " + key, needs: "ssh-keygen"},
	// ssh-keygen -H rewrites ~/.ssh/known_hosts: never run it without -f.
GO
cat >"$dir/ok.sh" <<'SH'
command -v ssh-keygen >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$tmp/key"
ssh-keygen -R "[127.0.0.1]:2222" -f "$tmp/known_hosts"
# ssh-keygen -H alone hashes ~/.ssh/known_hosts
SH
check || { echo "FAIL: runs on the test's own files, and mentions, were refused" >&2; exit 1; }

# shellcheck disable=SC2016 # planted lines, not to expand
for planted in \
	'ssh-keygen -H 2>&1 | head -1' \
	'ssh-keygen -R test-server' \
	'ssh-keygen -A' \
	'ssh-keygen -F test-server' \
	'ssh-keygen -H -f ~/.ssh/known_hosts' \
	'ssh-keygen -R host -f "$HOME/.ssh/known_hosts"' \
	'  out=$(ssh-keygen -l -F host)'; do
	printf '%s\n' "$planted" >"$dir/bad.sh"
	if check; then
		echo "FAIL: not caught in a script: $planted" >&2
		exit 1
	fi
	rm "$dir/bad.sh"
done
for planted in \
	'exec.Command("ssh-keygen", "-H").Run()' \
	'exec.Command("ssh-keygen", "-F", host).Output()' \
	'exec.Command("ssh-keygen", "-R", host, "-f", filepath.Join(home, ".ssh", "known_hosts"))' \
	'exec.Command("sh", "-c", "ssh-keygen -H")' \
	'bounded.Command(ctx, d, "ssh-keygen", "-A")'; do
	printf '%s\n' "$planted" >"$dir/bad_test.go"
	if check; then
		echo "FAIL: not caught in Go: $planted" >&2
		exit 1
	fi
	rm "$dir/bad_test.go"
done
echo "check-ssh-keygen: ok"
