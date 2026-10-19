# CNIL self-assessment: usage counting

The CNIL's self-assessment tool for audience measurement exempt from consent ("Outil d'auto-évaluation relatif à la mise en œuvre d'une solution de mesure d'audience exemptée de consentement", July 2025, from https://www.cnil.fr/fr/solutions-pour-la-mesure-daudience), filled in against Portenv's usage counting as implemented (PLAN.md 1.8, R-0010, R-0369): the app's check (`apps/mac/Sources/PortenvKit/UsageCheck.swift`), the counting Worker (`tools/counting/worker/`) and the owner's snapshot (`tools/counting/snapshot.mjs`).

- **Filled in:** 2026-10-19, by the counting lane, from the code in the counting PR. Run it again on the final implementation before each release that changes counting (PLAN.md 1.8, release checklist), and before the runner's counting ships (it isn't built yet).
- **What this is:** a design self-assessment, not legal advice. It is not a certification or a validation by the CNIL, and must never be described as "certified" or "validated" by the CNIL, nor carry its logo.
- **Who:** Portenv is its own audience-measurement "solution": one publisher, EverydayMoney SAS (the controller named on /privacy), measuring its own app. There is no measurement vendor. Cloudflare hosts the Worker and its database as a processor under its DPA, with its EU-US Data Privacy Framework certification as the transfer basis.
- **Beyond the exemption:** Portenv goes further than the exemption needs: counting is opt-in (R-0126). Nothing is sent until the person ticks "Count me in usage numbers".

## Purposes (tool §3)

The counting is limited to measuring the app's own audience: daily, weekly and monthly actives, retention by install month, and whether workspaces are moved, agents work in them, and how many workspaces and servers people use (activation and depth). It serves performance, server-capacity planning and understanding which features are used.

Explicitly excluded: any marketing measurement (conversion channels, campaigns, acquisition channels, ad fraud), and any cohort used to show people different content. The install month is a statistics dimension only; nothing in the app or on the server reads it to change what anyone sees.

## Criteria (tool §4 and the annex)

| Objective | Criterion | Recommended measure | Met | How, and any action |
| --- | --- | --- | --- | --- |
| The only purpose is audience measurement | Instructions to turn off anything outside that scope | N/A | Yes | There is nothing else to turn off: the check carries only the fields above, and the Worker only counts them. The person turns counting itself off in Settings › Updates. |
| | Data collected is minimised | HTTP header data minimised (major OS version, for example) | Yes | Ten published fields, all coarse: version, **macOS major version only** (`26`), architecture, install month, two flags (first check of the week, of the month) and, weekly, two yes-flags and two buckets (`boxes=1/2-5/6+`, `servers=0/1/2-3/4+`). The app sends a fixed `User-Agent: Portenv` and `Accept-Language: en`, never the Mac's. The Worker reads only the query string, never a header. |
| | At most three kinds of event | Presence; use of a feature; load and time statistics | Yes | One kind: a once-a-day presence check, sent only on a day a workspace was opened. The weekly flags say whether a feature was used that week (a move, an agent), not each use. |
| Provider as processor | Standard DPA under GDPR art. 28 | N/A | Yes | No measurement vendor. Cloudflare (hosting, Worker, D1) is a processor under its standard DPA. |
| | No pooling of raw data across clients | N/A | Yes | One publisher. Cloudflare doesn't receive raw checks to pool: the Worker keeps aggregate counters only. |
| | No reuse by the provider for its own purposes | N/A | Yes | Workers Logs, invocation logs and Logpush are off for the route; no Analytics Engine (`wrangler.toml`, tested). |
| | A contact for questions and complaints | N/A | Yes | /privacy names the controller and contact: EverydayMoney SAS, contact@everydaymoney.eu (website PR "Privacy: what usage counting sends"). |
| No tracking outside the app | No import of outside data | No CRM ID, UTM or campaign ID; referrer limited to host; no third-party integration | Yes | None collected; unknown parameters are never stored (tested). No third-party tool. |
| | No identifier usable across domains | First-party cookie; IP truncated if used; fingerprint salted per site and time-limited | Yes | No identifier at all: no cookie (the request is made without cookies), no install ID, no fingerprint. The IP address is never read or stored by the Worker (tested); Cloudflare sees it in transit only, as /privacy says. |
| | No cross-checking, deduplication or unified reach | Reach tools off | Yes | None exists. |
| Only anonymous statistics | All reports and exports are anonymous statistics | Aggregate and present to the nearest ten, or justify anonymity | Yes | Storage is per-day counters per field value (`counts(day, field, value, n)`), never a row per check or a combination of fields, except the one cross-tab install month × week active. Anything shown outside the owner hides cells from 1 to 9 (`snapshot.mjs --share`, tested). |
| | Anonymisation holds whatever the selection | A combination of criteria can't single out a user | Yes | Fields aren't stored together, so they can't be combined. The one cross-tab (install month × week) is hidden below 10 when shared. |
| | No tracking of one user's journey | Session replay off | Yes | Nothing per user exists to replay. |
| Right to object | An objection mechanism exists where personal data is processed | A button or link in the privacy policy | Yes | Opt-in: off until ticked, on Welcome (with the field list, before any check) and in Settings › Updates; one click turns it off and forgets the local counting state. /privacy says where. |
| | The refusal is respected over time | Opt-out cookie, or fingerprint on a deny list | Yes | The choice is kept in the app's own settings; while off, the app sends nothing at all. |

## Recommendations

- **Lifetime of tracers:** none is set: no cookie, no identifier. The app keeps only local dates (the install month, the last check day, the week and month whose first check was answered) and two yes-flags, never sent while counting is off.
- **Retention:** aggregate counts are anonymous and kept. Nothing request-level is stored or buffered at all (the CNIL's maximum is 25 months; the plan's target if raw checks were ever buffered is under 24 hours).
- **Information:** /privacy lists the exact fields, when they're sent, the controller and contact, Cloudflare's role, and how to turn counting off. First run lists the same fields before any check.

## Not covered yet

- The runner's own check (PLAN.md 1.8, "Servers count themselves too") isn't built; assess it when it is.
- Sparkle's update check (1.8) isn't built. When it carries the counting fields, it must send nothing else (Sparkle's system profile stays off), and this assessment is run again.
