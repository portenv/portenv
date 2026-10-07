# toolbox-node

The default toolbox image: Ubuntu 24.04, Node 22, PostgreSQL client, git, tmux, ripgrep, jq, build-essential, Python 3, Claude Code (stable channel) and `portenv-agent` as init. arm64 and amd64 from one Dockerfile.

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

Releases: push a tag `toolbox-node/vN` and CI publishes `ghcr.io/portenv/toolbox-node:vN` for both architectures.
