# 0009. Signing CLI releases: SSH signatures, plus build provenance

Date: 2026-10-07 · Status: proposed

## Context

Agents install the `portenv` CLI on their own computers through `portenv.com/cli`, a short script that downloads a release binary and must verify it before installing (milestone 2.6). Those computers are often shared or minimal (an agent's cloud computer, a CI runner): the verification must work with tools already there, without `sudo`, and must not depend on a long-lived key nobody can recover.

## Options

| Option | Verify with | Signing key | Fit |
| --- | --- | --- | --- |
| minisign | `minisign` (not installed by default anywhere) | One Ed25519 key | Good format, but the script would first have to install a verifier, unverified |
| Sigstore (cosign, keyless) | `cosign` (not installed by default) | None long-lived: GitHub's OIDC identity and a transparency log | Strong provenance, but the same bootstrap problem, and verification needs network access to Sigstore |
| **SSH signatures** | `ssh-keygen -Y verify` (OpenSSH 8.1 or later: every current macOS and Linux) | One Ed25519 key | Verifies with what every target already has |
| GPG | `gpg` (often missing on minimal machines) | One key | Heavier, same bootstrap problem on minimal images |

## Decision (proposed)

- **Every release's `checksums.txt` is signed with an SSH signature** (`ssh-keygen -Y sign -n portenv-release`) by a dedicated Ed25519 release key. The install script embeds the key in an `allowed_signers` line and runs `ssh-keygen -Y verify -n portenv-release` on the checksums file, then checks the binary's SHA-256 against it. Any mismatch stops the install.
- **GitHub build provenance attestations** are attached to every release binary as well (`actions/attest-build-provenance`), so anyone can check that a binary was built from this repository by its release workflow (`gh attestation verify`). The install script does not require them.
- **The release key** lives only in CI secrets and the owner's Keychain, with the same offline backup and restore test as the Sparkle key (milestone 1.8). Its public key is published in the install script, in the repository (`docs/release-key.pub`) and on `portenv.com`.
- **Rotation:** a new key is introduced by listing both keys in `allowed_signers` for one release, then dropping the old one.

## Consequences

- No extra tool is needed to verify a release; the install script stays short and runs without `sudo`.
- A lost release key is recoverable from the backup; a leaked one is rotated as above, and provenance attestations still show which builds came from CI.
