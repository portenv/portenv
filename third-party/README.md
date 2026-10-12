# Third-party notices

`gmake app` builds Portenv.app's third-party notices (R-0096) with `core/tools/notices`:

- the licence, copying, copyright, notice and patents files of every Go module compiled into the app's helpers (`portenv`, `portenvd`, `restic`), read from each binary's build information (what `go version -m` shows) and taken from the module cache at the exact version, always checked against Go's checksum database (the download sets `GOSUMDB=sum.golang.org` whatever the shell says, and ignores `GONOSUMDB`, `GOPRIVATE`, `GOINSECURE`, `GOFLAGS` and the go env file; it keeps this machine's `GOPROXY` and `GOMODCACHE`, read from `go env`);
- Go's own licence, for the standard library;
- the same files for every Swift package pinned in `apps/mac/Package.resolved`, from the build's checkouts at the pinned revision.

Files are found in every subfolder too (vendored code keeps its own licence beside it), except `.git`, `testdata` and `Tests`, and every file in a REUSE `LICENSES/` folder counts. Names matched: `LICENSE`, `LICENCE`, `LICENSES`, `UNLICENSE`, `COPYING`, `COPYRIGHT`, `NOTICE`, `PATENTS` and `THIRD_PARTY_NOTICES`, alone or with a suffix (`LICENSE-2.0.txt`, `LICENSE.APACHE`, `LICENSE_THIRD_PARTY`, `COPYING.md`).

They go into `Contents/Resources/Licenses/<go|swift>/<component>@<version>/<subfolder>/`, and `Contents/Resources/ThirdPartyNotices.txt` lists every component on one line (`name version · licence`), then each component's section: its licence, a `Source:` line (the module's zip on proxy.golang.org at that version, Go's source release, or the Swift package's repository at the pinned revision; MPL-2.0 requires it) and the texts. Help › Acknowledgements… shows that file. `scripts/release-local.sh` refuses an app whose notices don't cover its helpers and pins.

## What fails the build

- a component without a licence file (a `NOTICE`, `PATENTS` or `COPYRIGHT` alone isn't one);
- any licence file the tool doesn't recognise, each checked on its own, so a recognised `LICENSE` can't hide an unknown `COPYING`;
- copyleft terms in any file, NOTICE, COPYRIGHT and PATENTS included, in any case or spelling: GPL, LGPL or AGPL with a version (`GPL-2.0`, `GPL-2.0-or-later`, `GPLv3`, a Debian `License: GPL-2+`), "general public licence", "Affero", "ShareAlike", "share-alike" or "BY-SA". MPL-2.0's own definition of "Secondary License" is the one sentence ignored;
- vendored code without its licence: a folder at any depth whose `hash.txt` says "This directory is derived from … cloned from … at revision …", with no licence file in it. The vendoring scripts of swift-crypto and swift-nio-ssl leave BoringSSL's out;
- an app whose `Licenses/` folder for a component has no licence file, or whose `ThirdPartyNotices.txt` has no section for it.

## The allowlist

`allowlist.json` holds the reviewed entries; each needs the reviewer's agreement in the PR that adds it. An entry with a `licence` is an exception: the tool names the component's licence from it instead of identifying it. The copyleft and vendored-code checks still apply:

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

Without a `licence`, an entry narrows the checks instead. `extra` adds the licence of code a component vendors but leaves out; the added files are checked like the component's own, and each is tied to the revision its folder's `hash.txt` states, so an update that vendors another revision fails until the entry is updated:

```json
[
  {
    "kind": "swift",
    "name": "swift-nio-ssl",
    "version": "2.37.5",
    "extra": [{"text": "texts/boringssl-817ab07ebb53-LICENSE", "as": "Sources/CNIOBoringSSL/LICENSE", "revision": "817ab07ebb53da35afea409ab9328f578492832d"}],
    "reason": "BoringSSL's LICENSE at the vendored revision (hash.txt); the vendoring script leaves it out."
  }
]
```

`ignore` names files the reviewer has read, which skip identification and the copyleft check but still ship; the component's other files are checked as usual:

```json
[
  {
    "kind": "go",
    "name": "github.com/opencontainers/go-digest",
    "version": "v1.0.0",
    "ignore": ["LICENSE.docs"],
    "reason": "LICENSE.docs covers the documentation (CC BY-SA 4.0), which isn't compiled in."
  }
]
```

- `kind` is `go`, `go-std` or `swift`; `name` and `version` match exactly, so an update needs a new entry (and, for `extra`, the text at the new vendored revision).
- `texts` are files beside this README, added by hand, with distinct names; leave it out to keep the component's own files and only name the licence.
- `extra` texts go to `as` in the component's folder, inside a vendored folder, beside its own files, and can't replace one of them; `revision` is required.
- `ignore` names must be among the component's licence files, or the entry is stale and fails.
- Paths stay inside this folder: no absolute paths or `..`.
- `reason` says why the entry is safe.
