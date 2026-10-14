# shellcheck shell=bash
# SPDX-License-Identifier: Apache-2.0
#
# release-guards.sh: checks release-local.sh makes before it builds,
# sourced (not run), so each is tested on its own
# (scripts/test-attached-terminal.sh). Each prints why and returns 1 when
# the release must stop.

# The dev-only attached terminal (ADR 0020, decision 12) never reaches a
# release: ATTACHED_TERMINAL set in the environment stops it.
refuse_attached_terminal() {
	if [[ -n ${ATTACHED_TERMINAL:-} ]]; then
		echo "ATTACHED_TERMINAL is set: it's for a dev lane's build only, never a release" >&2
		return 1
	fi
}
