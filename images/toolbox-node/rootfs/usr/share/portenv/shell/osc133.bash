# SPDX-License-Identifier: Apache-2.0
#
# Shell integration for bash (OSC 133 semantic prompts): A when the prompt
# starts, B where the person types, C when a command starts, D;<exit> when it
# finishes. portenv-agent reads them from each tab's output (ADR 0017).
# Needs bash 4.4 or later (PS0). Sourced once per interactive shell.
if [ -n "${BASH_VERSION-}" ] && [[ $- == *i* ]] && [ -z "${__portenv_osc133-}" ] \
	&& (( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 4) )); then
	__portenv_osc133=1
	__portenv_ran=
	__portenv_osc133_precmd() {
		local ec=$?
		if [ -n "$__portenv_ran" ]; then printf '\033]133;D;%s\007' "$ec"; fi
		__portenv_ran=
		printf '\033]133;A\007'
		# B goes at the end of the prompt, whatever ~/.bashrc set PS1 to.
		case "$PS1" in *'133;B'*) ;; *) PS1="$PS1"'\[\033]133;B\007\]' ;; esac
		return "$ec"
	}
	PROMPT_COMMAND="__portenv_osc133_precmd${PROMPT_COMMAND:+; $PROMPT_COMMAND}"
	# PS0 is shown before a command runs; the assignment marks that one ran.
	PS0='${__portenv_ran:=1}'"$PS0"'\[\033]133;C\007\]'
fi
