# 0009. Signing CLI releases: SSH signatures, plus build provenance

Date: 2026-10-07 · Status: accepted

## Context

Agents install the `portenv` CLI on their own computers through `portenv.com/cli`, a short script that downloads a release binary and must verify it before installing (milestone 2.6). Those computers are often shared or minimal (an agent's cloud computer, a CI runner): the verification must work with tools already there, without `sudo`, and must not depend on a long-lived key nobody can recover.

## Options

| Option | Verify with | Signing key | Fit |
| --- | --- | --- | --- |
| minisign | `minisign` (not installed by default anywhere) | One Ed25519 key | Good format, but the script would first have to install a verifier, unverified |
| Sigstore (cosign, keyless) | `cosign` (not installed by default) | None long-lived: GitHub's OIDC identity and a transparency log | Strong provenance, but the same bootstrap problem, and verification needs network access to Sigstore |
| **SSH signatures** | `ssh-keygen -Y verify` (OpenSSH 8.1 or later: every current macOS and Linux) | One Ed25519 key | Verifies with what every target already has |
| GPG | `gpg` (often missing on minimal machines) | One key | Heavier, same bootstrap problem on minimal images |

## Decision

- **Every release's `checksums.txt` is signed with an SSH signature** (`ssh-keygen -Y sign -n portenv-release`) by a dedicated Ed25519 release key. The install script embeds the key in an `allowed_signers` line and runs `ssh-keygen -Y verify -n portenv-release` on the checksums file, then checks the binary's SHA-256 against it. Any mismatch stops the install.
- **GitHub build provenance attestations** are attached to every release binary as well (`actions/attest-build-provenance`), so anyone can check that a binary was built from this repository by its release workflow (`gh attestation verify`). The install script does not require them.
- **The release key** lives only in CI secrets, plus one offline backup the owner keeps, separate from the Sparkle key (milestone 1.8). It is never in the repository or on a developer machine.
- **Two keys are always trusted: the current one and the next one.** The install script and the repository (`docs/release-allowed-signers`) carry an `allowed_signers` file with both public keys. The next key is generated and backed up offline when the current one is first used, and is not given to CI until it is needed. Rotating therefore needs no emergency release: CI switches to the next key, and the following release adds a new next key.

**If the release key leaks:**

1. Switch CI to the next key at once, and remove the leaked key from `allowed_signers` in the repository and in the install script served at `portenv.com/cli`. Anyone installing from then on refuses binaries signed with the leaked key.
2. Re-sign `checksums.txt` for the current release with the next key and replace the asset, so the latest release still installs.
3. Check every published release against its build provenance attestation (`gh attestation verify`); any binary without a matching attestation from this repository's release workflow is treated as forged and removed.
4. Tell users: a security advisory on the repository and the website, naming the leaked key's fingerprint and the window of exposure, and asking anyone who installed in that window to reinstall from `portenv.com/cli`.
5. Generate a new next key, back it up offline, and add it to `allowed_signers` in the next release.

## Consequences

- No extra tool is needed to verify a release; the install script stays short and runs without `sudo`.
- A lost release key is recoverable from the backup; a leaked one is replaced by the next key without an emergency release, and provenance attestations still show which builds came from CI.
