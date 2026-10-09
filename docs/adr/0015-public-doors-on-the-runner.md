# 0015. Public doors on the user's own server: opt-in, one port or none

Date: 2026-10-09 · Status: accepted (the owner's decision)

## Context

The public launch ships Phases 1 and 2 together, and its demo has Dots (ChatGPT) and Meta Muse working in a box on the user's own server (PLAN.md, "Launch: Phases 1 and 2 together").

- **Dots** connects through remote MCP over HTTPS with OAuth, which ChatGPT's connectors require (milestone 2.7).
- **Muse** only has a browser, so it needs a web terminal (2.8).

Both have to be reachable from the internet. Until now Portenv opened no public or inbound port anywhere (CLAUDE.md's ground rules), and Phase 3's gateway was the only way to reach a box from outside. This ADR allows the user to open public doors on their own server, under strict conditions.

## Decision

**The user opens the doors, per server, behind one sheet that offers two ways:**
- **An inbound 443,** opened by the runner on that server.
- **No open port at all:** the user's own Cloudflare Tunnel or Tailscale Funnel, which only make outgoing connections. Portenv documents both.

The doors are off by default. The sheet says plainly what each way opens.

**Conditions** (each one has a test):

| Condition | Enforced by |
| --- | --- |
| **No hostname, no public doors.** ACME needs one, so the sheet asks for it. Certificates come from ACME and renew automatically. | The runner refuses to open the doors without a hostname that resolves to the server or the tunnel |
| **One port, 443.** It serves only the two public doors: remote MCP and the web terminal. | A test lists the server's listening ports before and after: only 443 is added |
| **The doors run as a separate, unprivileged process,** never inside the runner's root code. They reach a box only through the box agent's channel, like every other door. | The process's uid and capabilities are checked at start; a test proves it can't read the runner's state or keys |
| **Every request is authenticated,** except the OAuth discovery endpoints. | An endpoint test: every other path returns 401 without a valid token |
| **Remote MCP uses OAuth with per-agent sign-in.** The web terminal opens only with a one-time link that expires within minutes, tied to one agent and one box. | Link tests: a reused or expired link is refused, and so is a link for another box |
| **Rate limits apply,** and every request is logged with the agent's identity. | Load and log tests |
| **Per-agent attribution and revocation:** a revoked agent's sessions end at once. | As for SSH (ADR 0007) |
| **The vault rules hold** (ADR 0005). No door reaches encryption keys or passwords. | ADR 0005's tests, plus probes from each door |
| **"Close public doors"** in the app and the CLI closes both at once. They also close by themselves after 14 days without use (configurable). | A test with a fake clock |
| **Before launch:** the public endpoints are fuzzed in CI, and an outside security review covers the public doors. | The launch checklist (PLAN.md) |

**Users without a public server,** or who'd rather not open one, get Phase 3's Portenv-hosted versions (3.9, 3.10), which need no port on any of their machines.

## Consequences

- Opting in to an inbound 443 on one of the user's own servers is a deliberate, documented exception to "no inbound ports". The tunnel option keeps even that at zero.
- The public doors are new attack surface on the user's server. A separate unprivileged process, authentication on every request, short-lived links, automatic closing, fuzzing and an outside review are what keep it small.
- Phase 2 gains milestones 2.7 (remote MCP on the runner) and 2.8 (web terminal on the runner), and the launch gate depends on them.
