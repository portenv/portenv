# toolbox-node

The default toolbox image: Ubuntu 24.04, Node 22, PostgreSQL client, git, tmux, ripgrep, jq, build-essential, Python 3, Claude Code (stable channel, pinned by version), Chrome for Testing with Playwright's system libraries and fonts, and `portenv-agent` as init. arm64 and amd64 from one Dockerfile.

```sh
make image        # build for this machine's architecture only
make image-test   # build, then run test.sh against it
```

| Path | Becomes | Notes |
| --- | --- | --- |
| `Dockerfile` | the image | build from the repository root |
| `skel/` | `/usr/share/portenv/skel` | template for a new box's `/home/work` (plus Ubuntu's `/etc/skel`) |
| `rootfs/` | `/` | sudoers entry for `work`, shell environment in `/etc/profile.d` |
| `test.sh` | | checks an image on the native architecture |

The image's `/home` is empty. The agent creates `/home/work` from the skeleton only when the box is started with `PORTENV_INIT_HOME=1`, which Portenv sets for a brand-new box; otherwise a missing home is an error (ADR 0003).

## Browser checks

Chrome for Testing (Google's Chromium build for automation, stable channel) is at `/opt/chrome/chrome`, pinned by version and SHA-256 per architecture, and on `PATH` as `chromium`. Ubuntu 24.04's own chromium package only installs a snap, which can't run in a container, and Google Chrome has no Linux arm64 build; Chrome for Testing has both architectures. `CHROME_PATH`, `PUPPETEER_EXECUTABLE_PATH` and `PLAYWRIGHT_MCP_EXECUTABLE_PATH` point to it image-wide. Playwright reads no variable for it: pass `executablePath: process.env.CHROME_PATH` in `launchOptions`, or let a project's Playwright download its own browser into the home (`PLAYWRIGHT_BROWSERS_PATH`). The system libraries are those of `playwright install-deps chromium`, so that works without root.

**Sandbox.** Chromium's own sandbox needs user namespaces (or a setuid helper, which the image leaves out). A box never gets `CAP_SYS_ADMIN` (ADR 0005), and Docker's default seccomp profile then refuses them, so under Docker the browser starts only with `--no-sandbox`. Pass it per run (`chromium --headless --no-sandbox …`; Puppeteer `args: ['--no-sandbox']`); Playwright already launches Chromium without it unless `chromiumSandbox: true`. The image doesn't set it globally. The box is the isolation boundary here; still, run only pages you trust. `test.sh` prints, as an `info` line, whether the sandbox works on the engine it ran on.

Releases: push a tag `toolbox-node/vN` and CI publishes `ghcr.io/portenv/toolbox-node:vN` for both architectures.
