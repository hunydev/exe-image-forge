# AI contributor guide

This file is the canonical instruction set for AI coding agents working on Exe
Image Forge. Read it before changing the repository.

## Project goal

Exe Image Forge builds exe.dev-compatible development images, optionally adds
allowlisted AI CLI credentials, and serves short-lived, repository-scoped image
pull paths through a protected web application.

Keep the project suitable for public reuse. Do not optimize it for one person's
hostname, account, password, image namespace, or existing VM.

## Non-negotiable rules

- Write all user-facing text, documentation, code comments, test names, commit
  messages, and examples in English.
- Use only documented exe.dev features. Start with
  <https://exe.dev/docs.md>, especially
  <https://exe.dev/docs/proxy.md> and the documented private-image workflow.
  Do not call or depend on undocumented local endpoints.
- Never commit or print live passwords, tokens, cookies, credential files,
  private image URLs, exported authentication archives, or host configuration.
- Keep host-specific values outside Git in `/etc/exe-image-forge/forge.env` and
  `/etc/exe-image-forge/config.json`.
- Treat every `*/dev:*` image as a secret. Grant expiry prevents future pulls;
  it cannot revoke an image that has already been downloaded.
- Preserve unrelated working-tree changes. Inspect `git status` and the relevant
  diff before editing or staging.
- Do not weaken an authentication, credential, registry, path-validation,
  WebAuthn, CSP, or disk-safety boundary to make a feature easier to implement.

## Start here for web changes

There is no separate frontend framework or asset build step. The web UI is
plain HTML, CSS, and JavaScript embedded into the Go service.

| Area | Primary files |
| --- | --- |
| Image grant page | `vend/index.html` |
| Admin console | `vend/admin.html` |
| Shared passkey browser code | `vend/passkey.js` |
| Embedded asset declarations | `vend/ui.go` |
| Routes, grants, registry proxy, CSP | `vend/main.go` |
| Sessions, password login, terminal, bake APIs | `vend/admin.go` |
| WebAuthn/passkeys | `vend/webauthn.go` |
| Credential discovery and allowlisting | `vend/creds.go` |
| Vend service tests | `vend/*_test.go` |
| Browser tests | `e2e/forge.spec.js` |
| Browser test configuration | `playwright.config.js` |

`vend/ui.go` embeds the pages and browser assets with `go:embed`. Editing an
HTML or JavaScript file therefore requires rebuilding the vending binary before
the running site changes.

For a fixture-backed preview that never touches Docker or real credentials:

```bash
make dev
```

Open `http://127.0.0.1:18080` and sign in with `forge-demo`. Demo mode is
hard-limited to explicit loopback addresses and is marked in the UI.

## Web application contract

The browser reaches the Go service through the documented exe.dev HTTPS proxy:

```text
browser -> exe.dev HTTPS proxy -> 127.0.0.1:8000 vend service
                                      |
                                      +-> 127.0.0.1:5000 registry
```

Important routes:

| Route | Purpose |
| --- | --- |
| `/` | Authenticated image-grant UI |
| `/admin/` | Authenticated administration UI |
| `/healthz` | Local health check |
| `/api/info` | Repository and variant inventory |
| `/api/session` | Lightweight browser session state |
| `/api/grant` | Create a scoped, expiring image grant |
| `/api/creds` | Protected credential status |
| `/admin/api/*` | Protected login, terminal, bake, context, and passkey APIs |
| `/v2/t/<token>/...` | Read-only, repository-scoped registry proxy |

When adding a route, choose its authentication boundary explicitly and add a
test for both authorized and unauthorized requests.

### Authentication invariants

- The public and admin pages share an eight-hour HttpOnly, Secure,
  SameSite=Lax session.
- Before authentication, image selection, TTL, component options, grant
  creation, account details, terminal access, bake controls, and passkey
  management must not be visible or returned.
- Both pages check `/api/session` every second and use `BroadcastChannel` to
  propagate login and logout between tabs. Session expiry must be reflected
  without a reload.
- Password and passkey failures share the per-client rate limiter. Password
  hashing remains globally concurrency-bounded.
- Passkey registration requires an authenticated session. Passkey login does
  not.
- A grant token is random, read-only, limited to one configured repository, and
  valid for at most 24 hours.

### Frontend conventions

- Keep the current compact dark visual language and reuse the CSS variables in
  each page.
- Preserve responsive behavior at narrow widths and usable keyboard focus.
- Use semantic labels, buttons, tables, tab roles, and status messages.
- Keep network calls in the existing `api()` helpers and show actionable
  English errors.
- Do not add third-party CDN scripts or styles. Browser dependencies are pinned,
  stored under `vend/assets/`, embedded, and accompanied by license files.
- The response CSP is generated from embedded page content. If script loading,
  WebSocket behavior, or browser capabilities change, update CSP/security
  tests and keep the policy as narrow as possible.
- Avoid introducing a Node-based frontend toolchain unless the task truly
  requires it; the zero-build-step frontend is intentional.

## Backend and data boundaries

- The Go service intentionally uses the standard HTTP stack and embedded static
  files. Follow the existing handler style before adding dependencies.
- Do not hold `grantsMu` or the registry file lock across slow Docker, network,
  password-hashing, or subprocess work.
- Grant publication and registry garbage collection share
  `FORGE_REGISTRY_LOCK`. Garbage collection must stop the registry and restore
  it on failure or termination.
- New builds, bakes, and grants must continue to honor
  `FORGE_MIN_FREE_BYTES` and `FORGE_MAX_DISK_PERCENT`.
- Orphan cleanup may remove only provably Forge-created grant tags after
  `FORGE_ORPHAN_GRACE`. Preserve unrelated and recent tags.
- Credential baking is allowlist-only. Never replace `CRED_FILES` with a broad
  copy plus denylist, and never include prompts, histories, databases, caches,
  agent instruction files, or shell state.
- The local registry stays bound to loopback. External registry reads go only
  through the scoped vending route and accept only `GET` and `HEAD`.

## Other repository areas

| Path | Purpose |
| --- | --- |
| `exe-image-forge` | Host CLI for builds, auth, bake, verify, deploy, and GC |
| `install.sh` | Idempotent host installation and initial configuration |
| `appliance/` | Bootable exe.dev/GHCR image, first-boot services, and helpers |
| `image/Dockerfile` | The 16 selectable image variants |
| `image/files/` | Services and helpers installed into generated images |
| `deploy/` | Host systemd service and timer units |
| `forge.env.example` | Public host-configuration template |
| `config.example.json` | Public vending-configuration template |
| `docs/operations.md` | Detailed architecture and recovery guide |
| `docs/api.md` | Browser API request, response, and error contracts |
| `docs/ui.md` | Visual, responsive, accessibility, and copy conventions |

Keep the README, examples, CLI help, web copy, and operations guide consistent
when a user-visible command or behavior changes.

## Development workflow

Before editing:

```bash
git status -sb
rg '<relevant symbol or copy>' .
```

After editing, run the complete repository check:

```bash
make check
```

It runs Go tests with the race detector, `go vet`, Go formatting checks, and
Bash syntax checks. Add focused tests for changed behavior, especially:

- authentication and information disclosure
- session-gated UI markers and security headers
- credential selection and size/status reporting
- registry routing, grants, expiry, and reconciliation
- WebAuthn origin, relying-party, and signature-counter behavior
- shell-command construction and disk/GC safeguards

Also run `git diff --check` and review the final diff. Do not regenerate or
modify the vendored xterm distributions for unrelated UI work.

For web changes, also run the real-browser suite:

```bash
make e2e
```

It exercises desktop and mobile Chromium against the fixture-backed demo
server. Run `make screenshots` only for an intentional visual change, then
inspect `docs/images/` before staging it.

## Applying web changes to an installed VM

When the user asks for a web change to be reflected on the currently running
forge, source edits alone are not complete. After tests pass, rebuild and
restart the embedded web service from the configured checkout:

```bash
exe-image-forge vend-build
systemctl is-active exe-image-forge-vend.service
curl -fsS http://127.0.0.1:8000/healthz
```

Then inspect recent logs if the restart or health check fails:

```bash
journalctl -u exe-image-forge-vend.service -n 100 --no-pager
```

Deployment rules by change type:

| Change | Required live action |
| --- | --- |
| `vend/*.go`, embedded HTML/JS/CSS, favicon, assets | `exe-image-forge vend-build` |
| Host CLI only | Install through the existing project installer or the explicitly requested deployment workflow |
| `deploy/*.service` or `deploy/*.timer` | Reinstall units, run `systemctl daemon-reload`, then restart/enable the affected unit |
| `image/Dockerfile` or `image/files/*` | Rebuild the affected base variant; bake dev images only when credential inclusion is intended |
| Documentation/tests only | No service restart; verify the existing health endpoint |

Release workflow changes must keep the appliance distinct from generated
credentialed images. Never copy `/etc/exe-image-forge`,
`/var/lib/exe-image-forge`, a live checkout, or an authentication archive into
the appliance build context. First-boot secrets must be generated by
`appliance/initialize.sh`, not by the Dockerfile or GitHub Actions.

Do not display the live config or authentication home while deploying. Do not
make the exe.dev proxy public unless the user explicitly requests it.

## Completion checklist

A change is complete when:

1. The requested source behavior is implemented in English.
2. Relevant security and credential boundaries remain intact.
3. Focused tests and `make check` pass.
4. `make e2e` passes for web behavior or layout changes.
5. Documentation is updated when behavior changed.
6. The installed web service is rebuilt and healthy when live reflection was
   requested.
7. Only intended files are staged, and the GitHub result is reported when a
   commit or push was requested.
