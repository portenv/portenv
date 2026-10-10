# SPDX-License-Identifier: Apache-2.0
# Shell integration (OSC 133) for interactive bash login shells, which is
# what every tmux tab starts: existing homes get it too (ADR 0017).
if [ -n "${BASH_VERSION-}" ] && [ -r /usr/share/portenv/shell/osc133.bash ]; then
	. /usr/share/portenv/shell/osc133.bash
fi
