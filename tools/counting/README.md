# Usage counting (PLAN.md 1.8)

Downloads, repository traffic and active use, counted without identifying anyone (PLAN.md 1.8, R-0010, R-0126, R-0369). Three pieces:

| Piece | Where | Runs where |
| --- | --- | --- |
| The app's opt-in check | `apps/mac/Sources/PortenvKit/UsageCheck.swift` | the Mac, only for people who ticked "Count me in usage numbers" |
| The counting Worker | `tools/counting/worker/` | Cloudflare, on `portenv.com/appcast.xml` (the owner deploys it) |
| The download archive and the owner's snapshot | `scripts/archive-downloads.sh`, `scripts/counting-snapshot.sh`, `tools/counting/snapshot.mjs` | the owner's Mac |

Nothing here holds a credential. GitHub is read with the owner's `gh` sign-in and Cloudflare with his `wrangler login`; the data they produce stays in `~/Portenv-metrics` (or `$PORTENV_METRICS_DIR`), outside this repository.

## What the app sends

Only when the person ticked "Count me in usage numbers" (off until ticked, on Welcome and in Settings › Updates), at most once a day, and only on a day a workspace was opened: one `GET https://portenv.com/appcast.xml?…` with these fields and nothing else.

| Field | Value | When |
| --- | --- | --- |
| `v` | Portenv's version, like `0.2.0` | every check |
| `os` | macOS major version, like `26` | every check |
| `arch` | `arm64` or `x86_64` | every check |
| `c` | install month, like `2026-10` | every check |
| `w` | `1` | the first check of each ISO week |
| `m` | `1` | the first check of each month |
| `moved` | `1` if a workspace moved since the last weekly check | weekly check only |
| `agent` | `1` if an agent ran in a workspace since the last weekly check | weekly check only |
| `boxes` | `1`, `2-5` or `6+` workspaces | weekly check only |
| `servers` | `0`, `1`, `2-3` or `4+` connected servers | weekly check only |

No identifier of any kind, no cookie, a fixed `User-Agent: Portenv` and `Accept-Language: en`, a 5-second limit, off the main thread, and the redirect is never followed. If portenv.com doesn't answer, the count is lost and nothing waits; the week's and month's flags go on the next day's check. A dev lane's build (`gmake app LANE=X`) never counts. Off forgets the local counting state.

## The Worker

`worker/src/index.mjs` reads the query string only (never the IP address, never a header), turns a valid check into one counter per field value (`worker/src/count.mjs`), adds them to D1 after answering, and redirects to the appcast on the latest GitHub release. A request without the four base fields (a plain appcast fetch) counts nothing.

**Storage** (`worker/schema.sql`): one table, `counts(day, field, value, n)`: per UTC day, a count per field value, plus `checks` (daily actives) and `cw`, install month × week active (the one cross-tab the snapshot needs, R-0010 point 1). Never a row per check, never a row combining fields. The counts are anonymous aggregates and are kept; nothing request-level is stored, buffered or logged.

**Logs:** `wrangler.toml` turns Workers Logs and invocation logs off, Logpush off, no tail consumers, no Analytics Engine, no workers.dev or preview URL (checked by `test/worker.test.mjs`).

**Tests:** `gmake counting-test` (node's own test runner, no packages).

## Deploy (the owner)

Cloudflare account access and the deploy are the owner's. Nothing below runs in CI, and no Cloudflare credential goes into this repository or the board. Go live in this order, the same day:

1. **Sign in:** `cd tools/counting/worker && npx --yes wrangler@4.145.0 login` (opens the browser; the token stays in wrangler's own config on this Mac).
2. **Make the database:** `npx --yes wrangler@4.145.0 d1 create portenv-counting`. Put the printed `database_id` in `wrangler.toml` in place of `SET-AT-DEPLOY` (it names the database; it isn't a secret, but leave it out of commits if you prefer and keep the edit local).
3. **Make the table:** `npx --yes wrangler@4.145.0 d1 execute portenv-counting --remote --file schema.sql`.
4. **Free the route:** in the Cloudflare dashboard, portenv.com › Rules › Redirect Rules: if a rule redirects `/appcast.xml`, delete it or exclude `/appcast.xml` from it (redirect rules run before Workers; the Worker does the redirect now). Leave `/download`'s rule as it is.
5. **Check zone logs:** portenv.com › Analytics & Logs › Logpush: no job may include `/appcast.xml` requests (an HTTP-requests job would export IPs for the route). Remove or filter any that do.
6. **Deploy:** `npx --yes wrangler@4.145.0 deploy`. Then in the dashboard, Workers & Pages › portenv-counting › Settings › Observability: Workers Logs shows as disabled, and Logpush as off.
7. **Check it:** `curl -sI 'https://portenv.com/appcast.xml'` answers `302` to the GitHub appcast; `curl -sI 'https://portenv.com/appcast.xml?v=0.0.0&os=26&arch=arm64&c=2026-10'` too, then `npx --yes wrangler@4.145.0 d1 execute portenv-counting --remote --command "SELECT * FROM counts"` shows the test check's counters. Delete them: `… --command "DELETE FROM counts WHERE field='v' AND value='0.0.0'"` (and the others from that day if no real check came yet).
8. **Same day:** merge the website's privacy PR ("Privacy: what usage counting sends") and this repository's counting PR. The privacy page and the app's text must be live together with the Worker.

To stop counting at any time: `npx --yes wrangler@4.145.0 delete portenv-counting` and restore the `/appcast.xml` redirect rule. The app's check then lands on the redirect and is counted nowhere.

## The download archive (daily)

`scripts/archive-downloads.sh` saves GitHub's release download counts, views, clones, referrers, popular paths and stars to `~/Portenv-metrics` (raw JSON per day, plus merged CSVs). GitHub keeps traffic for 14 days, so it should run daily; a missed day is recovered within two weeks. It needs `gh` signed in with access to the repository, and `jq`.

Nothing is scheduled for you. To run it daily at 08:00 with launchd, save this as `~/Library/LaunchAgents/com.portenv.metrics-archive.plist` (with your clone's path), then `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.portenv.metrics-archive.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.portenv.metrics-archive</string>
  <key>ProgramArguments</key>
  <array><string>/bin/bash</string><string>-lc</string><string>"$HOME/Documents/GitHub/portenv/scripts/archive-downloads.sh"</string></array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>8</integer><key>Minute</key><integer>0</integer></dict>
  <key>StandardErrorPath</key><string>/tmp/portenv-metrics-archive.log</string>
</dict>
</plist>
```

launchd runs a missed time when the Mac wakes. Or with cron: `0 8 * * * "$HOME/Documents/GitHub/portenv/scripts/archive-downloads.sh"`.

Cloudflare's own count for `portenv.com/download` (PLAN.md 1.8) is in the dashboard (portenv.com › Analytics); it isn't archived here.

## The owner's snapshot

`scripts/counting-snapshot.sh` reads the counters (your `wrangler login`) and the archive, and writes `~/Portenv-metrics/snapshot/<date>/` with `daily.csv` (downloads, new downloads, daily actives, weekly actives week to date, monthly actives month to date), `cohorts.csv` (actives per install month per ISO week) and `snapshot.html`, a page you open locally. It's for you: small cells aren't hidden. For anything shown to others (a deck, the website), run it with `--share`, which shows every cell from 1 to 9 as `<10` (R-0010 point 2).
