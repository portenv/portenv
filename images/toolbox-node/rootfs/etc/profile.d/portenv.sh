# SPDX-License-Identifier: Apache-2.0
# Keep user-installed tools in the home so they travel with the box.
export NPM_CONFIG_PREFIX="$HOME/.local"
export PLAYWRIGHT_BROWSERS_PATH="$HOME/.local/share/ms-playwright"
case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) export PATH="$HOME/.local/bin:$PATH" ;;
esac
# Claude Code is pinned by the image: no update check or "Update
# available" notice in the owner's own shells either.
export DISABLE_AUTOUPDATER=1
