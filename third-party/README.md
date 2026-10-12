# Third-party notices

`gmake app` builds Portenv.app's third-party notices (R-0096) with `core/tools/notices`:

- the licence, copying, copyright, notice and patents files of every Go module compiled into the app's helpers (`portenv`, `portenvd`, `restic`), read from each binary's build information (what `go version -m` shows) and taken from the module cache at the exact version, always checked against the checksum database (the download ignores `GONOSUMDB`, `GOPRIVATE`, `GOINSECURE`, `GOFLAGS` and the go env file, and refuses `GOSUMDB=off`);
- Go's own licence, for the standard library;
- the same files for every Swift package pinned in `apps/mac/Package.resolved`, from the build's checkouts at the pinned revision.

Files are found in every subfolder too (vendored code keeps its own licence beside it), except `.git`, `testdata` and `Tests`, and every file in a REUSE `LICENSES/` folder counts. Names matched: `LICENSE`, `LICENCE`, `LICENSES`, `UNLICENSE`, `COPYING`, `COPYRIGHT`, `NOTICE` and `PATENTS`, alone or with a suffix (`LICENSE-2.0.txt`, `LICENSE.APACHE`, `COPYING.md`).

They go into `Contents/Resources/Licenses/<go|swift>/<component>@<version>/<subfolder>/`, and `Contents/Resources/ThirdPartyNotices.txt` lists every component with its version and licence, then each component's section: its licence, a `Source:` line (the module's zip on proxy.golang.org at that version, Go's source release, or the Swift package's repository at the pinned revision; MPL-2.0 requires it) and the texts. Help › Acknowledgements… shows that file. `scripts/release-local.sh` refuses an app whose notices don't cover its helpers and pins.

## What fails the build

- a component without a licence file (a `NOTICE`, `PATENTS` or `COPYRIGHT` alone isn't one);
- any licence file the tool doesn't recognise, each checked on its own, so a recognised `LICENSE` can't hide an unknown `COPYING`;
- copyleft terms in any file ("GNU General Public", "GNU Lesser", "GNU Library General Public", "Affero", "ShareAlike"; MPL-2.0's own definition of "Secondary License" is the one sentence ignored);
- a Swift package that vendors another project (a `Sources/<target>/hash.txt` saying "This directory is derived from …") with no licence file in that folder: the vendoring scripts of swift-crypto and swift-nio-ssl leave BoringSSL's out;
- an app whose `Licenses/` folder for a component has no licence file, or whose `ThirdPartyNotices.txt` has no section for it.

## The allowlist

`allowlist.json` holds the reviewed entries; each needs the reviewer's agreement in the PR that adds it. An entry with a `licence` is an exception: the tool names the component's licence from it instead of identifying it, and doesn't look for copyleft terms:

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

An entry with only `extra` adds the licence of code a component vendors but leaves out; the added files are checked like the component's own:

```json
[
  {
    "kind": "swift",
    "name": "swift-nio-ssl",
    "version": "2.37.5",
    "extra": [{"text": "texts/boringssl-817ab07ebb53-LICENSE", "as": "Sources/CNIOBoringSSL/LICENSE"}],
    "reason": "BoringSSL's LICENSE at the vendored revision (hash.txt); the vendoring script leaves it out."
  }
]
```

- `kind` is `go`, `go-std` or `swift`; `name` and `version` match exactly, so an update needs a new entry (and, for `extra`, the text at the new vendored revision).
- `texts` are files beside this README, added by hand, with distinct names; leave it out to keep the component's own files and only name the licence.
- `extra` texts go to `as` in the component's folder, beside its own files, and can't replace one of them.
- Paths stay inside this folder: no absolute paths or `..`.
- `reason` says why the entry is safe.
