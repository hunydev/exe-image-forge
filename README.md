# Exe Image Forge

Build exe.dev-compatible development images with current AI coding CLIs,
optionally bake in your authenticated CLI state, and issue time-limited Docker
pull URLs from your own exe.dev VM.

> [!WARNING]
> A credentialed image contains live credentials. Anyone who pulls it can
> extract and use them even after the download URL expires. Keep the forge
> private, use short grants, and never push credentialed images to a public
> registry.

Exe Image Forge is an independent community project, not an official exe.dev
product.

## Features

- Ubuntu 24.04 with systemd, configured for exe.dev custom images
- GitHub CLI, Node.js, Python, and `uv` in every variant
- Independently selectable Codex CLI, Claude Code, Gemini CLI, and Go
- Separate logged-out `base` images and credentialed `dev` images
- Persistent authentication state stored outside the Git checkout and images
- A credential allowlist that excludes prompts, histories, logs, and local
  agent state
- Password and passkey authentication with per-client login throttling
- A session-gated web UI whose image controls stay hidden until sign-in
- Immediate session expiry and sign-out detection across open browser tabs
- A tabbed admin console for CLI logins, images, security, and passkeys
- Self-hosted terminal assets, a strict content security policy, and defensive
  HTTP headers
- A read-only, token-scoped registry proxy with expiring grants
- Disk-pressure guards, orphan reconciliation, and safe registry garbage
  collection
- Daily CLI updates inside running images and a weekly forge refresh timer

## Requirements

- An Ubuntu-based exe.dev VM
- Docker with Buildx, Go, Git, Python 3, `curl`, and `sudo`
- The exe.dev `exedev` user
- Port 8000 selected for the VM HTTPS proxy
- Enough disk for the variants you plan to build; 20 GB or more is practical

Only documented exe.dev interfaces are used. See the official
[HTTP proxy](https://exe.dev/docs/proxy) and
[private registry](https://exe.dev/docs/private-image) documentation.

## Quick start

On the forge VM:

```bash
git clone https://github.com/hunydev/exe-image-forge.git
cd exe-image-forge
./install.sh

exe-image-forge build
exe-image-forge auth all
exe-image-forge bake
exe-image-forge verify
```

The installer prompts for a web password and stores only a salted PBKDF2 hash
in `/etc/exe-image-forge/config.json`. It does not build the large images.

Use installer options when the public hostname, local image namespace, or
exe.dev VM name differs from the defaults:

```bash
./install.sh \
  --pull-host images.example.com \
  --image-prefix my-forge \
  --vm-name my-forge-vm
```

From your laptop, select port 8000 for the documented exe.dev HTTPS proxy:

```bash
ssh exe.dev share port <forge-vm-name> 8000
```

Open `https://<forge-vm-name>.exe.xyz/`, sign in, choose an image and TTL, and
copy the generated `ssh exe.dev new --image=...` command.

The proxy is private by default. If you deliberately make it public with
`share set-public`, the forge's password/passkey and expiring registry token
still apply.

## Authentication workflow

`exe-image-forge auth` opens each provider's supported interactive login flow
inside the base image while using the persistent authentication home:

```bash
exe-image-forge auth gh
exe-image-forge auth codex
exe-image-forge auth claude
exe-image-forge auth gemini
# Or run all four in sequence:
exe-image-forge auth all
```

| Tool | Login flow |
| --- | --- |
| GitHub CLI | `gh auth login --git-protocol https` |
| Codex CLI | `codex login --device-auth` |
| Claude Code | `claude auth login` with the returned code pasted into the terminal |
| Gemini CLI | Browser-disabled OAuth with the returned code pasted into the terminal |

If Gemini redirects to an unreachable localhost callback, use the relay helper
shown by the CLI:

```bash
exe-image-forge relay '<callback-url>'
```

Check the detected credentials, bake them into the selected `dev` variants,
and verify the result:

```bash
exe-image-forge status
exe-image-forge bake
exe-image-forge verify
```

The admin page provides the same login flows in a browser terminal and checks
Codex, Claude, and Gemini credential sizes before baking. `base` images contain
the tools but no login state. Only the allowlisted credential files are copied
into `dev` images.

## Image variants

Codex, Claude, Gemini, and Go are independently selectable. The 16 combinations
use these tag rules:

| Agent selection | Tag prefix/base |
| --- | --- |
| Neither Codex nor Claude | `core` |
| Codex only | `codex` |
| Claude only | `claude` |
| Codex and Claude | `min` (historical compatibility name) |

Gemini and Go add `-gemini`, `-go`, or `-go-gemini`. The historical `gemini`,
`go`, and `go-gemini` tags still mean Codex and Claude plus those components.

Build one combination or all 16:

```bash
exe-image-forge build codex-go
exe-image-forge build
```

Use `--fresh` when you need a build with no Docker layer cache:

```bash
exe-image-forge build --fresh codex-go
```

## Web UI and image grants

The public page shows only the sign-in form until an authenticated session
exists. Image selection, TTL, options, and grant creation are revealed after
sign-in. Sessions last eight hours and the UI checks their state every second,
so expiry or sign-out is reflected without a page reload.

Each grant:

- is limited to one configured repository
- uses a random 128-bit bearer token
- accepts registry `GET` and `HEAD` requests only
- expires after the selected TTL, up to 24 hours
- receives a unique tag and digest without duplicating shared parent layers

Expiry removes the route and registry tag. It cannot revoke an image that has
already been pulled.

## Configuration

Host-specific settings live in `/etc/exe-image-forge/forge.env`, outside the
repository. See [`forge.env.example`](forge.env.example). The main settings are:

| Setting | Default | Purpose |
| --- | --- | --- |
| `FORGE_ROOT` | Checkout directory at install time | Source and Docker build context |
| `FORGE_IMAGE_PREFIX` | `exe-image-forge` | Local `base`/`dev` image namespace |
| `FORGE_VM_NAME` | Current hostname | exe.dev VM name used in generated commands |
| `FORGE_CONFIG` | `/etc/exe-image-forge/config.json` | Hashed password and vending configuration |
| `FORGE_STATE` | `/var/lib/exe-image-forge/grants.json` | Persistent grant metadata |
| `FORGE_AUTH_HOME` | `/var/lib/exe-image-forge/authhome` | Persistent CLI credentials |
| `FORGE_REGISTRY_DATA` | `/var/lib/exe-image-forge/registry` | Private registry blob storage |
| `FORGE_REGISTRY_LOCK` | `/var/lib/exe-image-forge/registry.lock` | Shared grant and GC lock |
| `FORGE_MIN_FREE_BYTES` | `2147483648` | Minimum free bytes required for builds and grants |
| `FORGE_MAX_DISK_PERCENT` | `90` | Maximum filesystem usage allowed for builds and grants |
| `FORGE_BUILD_CACHE_MIN_FREE` | `5gb` | Free-space target used by Buildx cache GC |
| `FORGE_BUILD_CACHE_RESERVED` | `2gb` | Buildx cache space retained by GC |
| `FORGE_ORPHAN_GRACE` | `2h` | Minimum age before an unknown Forge grant tag is removed |

Do not put the web password or provider tokens in `forge.env`. Change the
password through the protected prompt:

```bash
exe-image-forge password
```

If a private exe.dev proxy requires a VM token for pulls from another machine,
store it without adding it to the repository:

```bash
exe-image-forge token
```

## Command reference

```text
exe-image-forge build [--fresh] [variant]  Build one or all base variants
exe-image-forge sizes                       Show variant sizes
exe-image-forge refresh                     Update CLIs, rebuild, and re-bake
exe-image-forge versions [image]            Show versions baked into an image
exe-image-forge context [variant]           Show the generated agent context
exe-image-forge auth <tool>                 Log in: gh, codex, claude, gemini, all
exe-image-forge relay <url>                 Replay a Gemini localhost callback
exe-image-forge import [file]               Import an authentication archive
exe-image-forge export-hint                 Show the remote export command
exe-image-forge shell                       Open a shell with persistent auth state
exe-image-forge status                      Show detected credentials
exe-image-forge bake [variant]              Build credentialed dev images
exe-image-forge verify [image]              Check tools and authentication
exe-image-forge password                    Change the web password
exe-image-forge token [token]               Store an exe.dev VM pull token
exe-image-forge vend-build                  Rebuild and restart the web service
exe-image-forge grants                      List active image grants
exe-image-forge gc [--dry-run]              Safely prune image and registry data
```

## Maintenance and recovery

Systemd timers run a weekly `refresh` and daily garbage collection. Registry GC
takes the same lock used by grant creation, stops the local registry, runs the
collector, and restarts the registry even if collection fails. Preview all
cleanup decisions without deleting data:

```bash
exe-image-forge gc --dry-run
```

The vending service also reconciles old Forge-labeled tags that no longer have
matching grant state. Unrelated repository tags and recent unknown tags are
preserved. Builds and new grants stop before configured disk-pressure limits
are crossed.

Useful checks:

```bash
systemctl status exe-image-forge-vend.service
systemctl list-timers 'exe-image-forge-*'
curl -fsS http://127.0.0.1:8000/healthz
exe-image-forge grants
exe-image-forge sizes
```

See the [operations guide](docs/operations.md) for recovery procedures and
details about authentication, updates, security, and registry storage.

## Architecture

```text
browser ──HTTPS──> exe.dev proxy ──> :8000 vending service
                                          |
                                          +─ password/passkey admin UI
                                          +─ expiring /v2/t/<token>/... proxy
                                                                  |
                                                                  v
                                                     127.0.0.1:5000 registry
```

The registry binds only to loopback. The web service is the authenticated
control plane and the only externally reachable registry path.

## Development

```bash
make check
```

This runs Go tests with the race detector, `go vet`, Go formatting checks, and
Bash syntax checks. The test suite covers authentication and credential
filtering, grants and registry cleanup, passkeys, security headers, session
gating, and command integration. GitHub Actions runs the same validation on
pushes and pull requests.

Contributions are welcome; see [`CONTRIBUTING.md`](CONTRIBUTING.md). Security
issues should follow [`SECURITY.md`](SECURITY.md).

## License

Apache-2.0. The image is derived in part from
[boldsoftware/exeuntu](https://github.com/boldsoftware/exeuntu); see
[`NOTICE`](NOTICE).
