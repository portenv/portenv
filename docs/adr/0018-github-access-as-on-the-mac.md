# 0018. GitHub in a box: gh's normal login, the same access as on the Mac

Date: 2026-10-10 · Status: accepted (the owner, 2026-10-10, choosing option 2 of R-0047)

## Context

Link GitHub (1.5, boards `link-github` and `link-github-linked`) signs a box in to GitHub. The first boards promised per-repository access ("6 repositories allowed · Change…", "for the repositories the person picks"). `gh auth login` can't keep that promise. It signs in through GitHub CLI's own OAuth app, whose scopes (`repo` and others) cover every repository the account can reach.

Two honest options were on the table:
1. a Portenv GitHub App: the person picks repositories when installing it, and its user tokens reach only those (8-hour tokens, refreshed in the box);
2. gh's normal login: the same access the person's gh has on their Mac.

## Decision

Option 2. GitHub access in a box works exactly as on the person's Mac: plain `gh auth login` with the device flow and gh's default scopes, then `gh auth setup-git`, so plain `git clone` and `git push` work. Any number of repositories can be cloned into the box afterwards. There's no Portenv GitHub App and no repository picker.

These stay as before:
- The login lives in the box home's gh config (`hosts.yml`), in the box's encrypted home, and moves with the box. Portenv's servers never see it, and it's never in an image.
- It isn't a Portenv account.

## Consequences

- **The words are the truth.** The boards say "the same access as on your Mac" and drop the picker and its count.
- **Agents in the box get that access too.** The Linked dialog's warning says so ("with the same access as you"). The inspector's Who's here shows the login, with Sign Out of GitHub.
- **No GitHub App to register, and no token refresh.** Nothing on GitHub is owned by Portenv.
- **For narrower access, the person can still choose.** In the box they can sign in with a fine-grained personal access token (`gh auth login --with-token`). That's gh's own option, not a Portenv feature. If people ask for narrower access, option 1 can be added later without changing where the login lives.
