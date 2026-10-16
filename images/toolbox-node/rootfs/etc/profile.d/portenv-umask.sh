# SPDX-License-Identifier: Apache-2.0
#
# The owner's files at the top of their home are private (ADR 0022 §2,
# R-0262): agents can pass through /home/work, so a new file there must
# not be readable by others. The project keeps its default ACLs, which
# decide the mode of files made inside it whatever the umask.
if [ "$(id -u)" = 1000 ]; then
	umask 077
fi
