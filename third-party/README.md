# Third-party notices

`gmake app` builds Portenv.app's third-party notices (R-0096) with `core/tools/notices`:

- the licence, copying, notice and patents files of every Go module compiled into the app's helpers (`portenv`, `portenvd`, `restic`), read from each binary's build information (what `go version -m` shows) and taken from the checksum-verified module cache at the exact version;
- Go's own licence, for the standard library;
- the same files for every Swift package pinned in `apps/mac/Package.resolved`, from the build's checkouts at the pinned revision.

They go into `Contents/Resources/Licenses/<go|swift>/<component>@<version>/`, and `Contents/Resources/ThirdPartyNotices.txt` lists every component with its version and licence, followed by the texts. Help › Acknowledgements… shows that file. `scripts/release-local.sh` refuses an app whose notices don't cover its helpers and pins.

## The allowlist

A component without a licence file, or with a licence the tool doesn't recognise, fails the build. `allowlist.json` holds the reviewed exceptions; each needs the reviewer's agreement in the PR that adds it:

```json
[
  {
    "kind": "go",
    "name": "example.com/module",
    "version": "v1.2.3",
    "licence": "MIT",
    "texts": ["texts/example.com-module-LICENSE.txt"],
    "reason": "The repository states MIT in its README but has no LICENSE file; text added by hand from that statement."
  }
]
```

- `kind` is `go`, `go-std` or `swift`; `name` and `version` match exactly, so an update needs a new entry.
- `texts` are files beside this README, added by hand; leave it out to keep the component's own files and only name the licence.
- `reason` says why the exception is safe.
