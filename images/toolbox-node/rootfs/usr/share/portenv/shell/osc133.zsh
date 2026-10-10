# SPDX-License-Identifier: Apache-2.0
#
# Shell integration for zsh (OSC 133 semantic prompts): A when the prompt
# starts, B where the person types, C when a command starts, D;<exit> when it
# finishes. portenv-agent reads them from each tab's output (ADR 0017).
if [[ -o interactive ]] && [[ -z ${_portenv_osc133-} ]]; then
	_portenv_osc133=1
	_portenv_ran=
	autoload -Uz add-zsh-hook
	_portenv_osc133_precmd() {
		local ec=$?
		[[ -n $_portenv_ran ]] && printf '\e]133;D;%s\a' $ec
		_portenv_ran=
		printf '\e]133;A\a'
		[[ $PS1 == *'133;B'* ]] || PS1+=$'%{\e]133;B\a%}'
	}
	_portenv_osc133_preexec() {
		_portenv_ran=1
		printf '\e]133;C\a'
	}
	add-zsh-hook precmd _portenv_osc133_precmd
	add-zsh-hook preexec _portenv_osc133_preexec
fi
