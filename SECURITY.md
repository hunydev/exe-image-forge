# Security policy

## The credential boundary

Exe Image Forge intentionally creates images containing authenticated CLI
credentials. Treat every credentialed image as a secret:

- A grant expiration prevents future pulls; it cannot revoke an image already
  downloaded.
- Do not push `*/dev:*` images to a public or shared registry.
- Use a dedicated forge VM and restrict access to its persistent data.
- Revoke provider credentials if an image or registry is exposed.

Only files in the `CRED_FILES` allowlist in `exe-image-forge` are copied into a
baked image. Conversation databases, prompt logs, caches, and shell history are
excluded by default.

The web password is PBKDF2-HMAC-SHA256 hashed with a random salt. The plaintext
password should never be stored in `forge.env`, `config.json`, or the repository.

## Automated checks

Pushes, pull requests, and a weekly schedule run:

- CodeQL analysis for Go
- `govulncheck` against reachable Go symbols
- full-history Gitleaks scanning with redacted output
- ShellCheck and Hadolint
- dependency review for pull requests

GitHub secret scanning and push protection should remain enabled. Dependabot
tracks Go, npm, Docker, and GitHub Actions dependencies.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting for this repository. Do not
include live tokens, credentials, private image URLs, or exported auth files in
an issue.
