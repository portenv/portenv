#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# test-scan-excerpt.sh: a planted token never reaches the issue text that
# scan-excerpt.sh builds (run by make test).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
upper=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n' | tr 'a-f' 'A-F')
named="not-hex-but-secret-$RANDOM"
cat > "$tmp/log" <<EOF
=== RUN   TestChannelTokenNotOnDisk
    channel_fullscan_test.go:52: the token is in a file in the box: /opt/x (it was ${token})
    channel_fullscan_test.go:53: also ${upper} and token=${token}x
-----BEGIN PRIVATE KEY-----
${token}
abcdef
-----END PRIVATE KEY-----
    some line with ${named} in it
--- FAIL: TestChannelTokenNotOnDisk (41.2s)
FAIL
EOF

out=$(REDACT="$named" "$here/scan-excerpt.sh" "$tmp/log")
fail=0
for s in "$token" "$upper" "$named" "abcdef" "PRIVATE KEY"; do
	if grep -qiF -- "$s" <<<"$out"; then
		echo "scan-excerpt leaked: ${s:0:6}…" >&2
		fail=1
	fi
done
for s in "--- FAIL: TestChannelTokenNotOnDisk" "/opt/x" "[redacted]" "[redacted PEM block]"; do
	if ! grep -qF -- "$s" <<<"$out"; then
		echo "scan-excerpt dropped: $s" >&2
		fail=1
	fi
done
exit "$fail"
