# Exe Image Forge

Build exe.dev-compatible development images with current AI coding CLIs, bake
your authenticated CLI state into them, and vend time-limited Docker pull URLs
from your own exe.dev VM.

> [!WARNING]
> A baked image contains live credentials. Anyone who pulls it can extract and
> use them even after its download URL expires. Keep the forge private, use
> short grants, and never push credentialed images to a public registry.

Exe Image Forge is an independent community project, not an official exe.dev
product.

## What it provides

- Ubuntu 24.04 + systemd, shaped for exe.dev custom images
- Codex CLI, Claude Code, GitHub CLI, Node.js, Python, and `uv`
- Optional Go and Gemini CLI variants with shared image layers
- A persistent authentication home kept outside rebuilt images
- An explicit credential allowlist that excludes prompts, histories, and logs
- A password/passkey-protected web UI for login, bake, and image grants
- A read-only, token-scoped registry proxy with grant expiration
- Daily in-image CLI updates and weekly forge refresh timers

The base images are logged out. The `dev` images are the credentialed artifacts
intended for your own use.

## Requirements

- An Ubuntu-based exe.dev VM with Docker, Docker Buildx, Go, Git, and `sudo`
- Port 8000 selected for the VM HTTPS proxy
- Enough disk for several shared image variants (20 GB or more is practical)

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

`install.sh` asks for the web password without placing it in an argument,
environment variable, repository file, or shell history. It stores only a
salted PBKDF2 hash in `/etc/exe-image-forge/config.json`.

From your laptop, select port 8000 for the documented exe.dev HTTPS proxy:

```bash
ssh exe.dev share port <forge-vm-name> 8000
```

Then open `https://<forge-vm-name>.exe.xyz/`, authenticate, choose a variant,
and copy the generated `ssh exe.dev new --image=...` command.

The proxy is private by default. If you deliberately make it public with
`share set-public`, the forge's own password/passkey and expiring registry
token still apply.

## Configuration

Host-specific settings live in `/etc/exe-image-forge/forge.env`, outside Git.
See [`forge.env.example`](forge.env.example). Important settings include:

| Setting | Default | Purpose |
| --- | --- | --- |
| `FORGE_ROOT` | checkout directory at install time | Source and Docker build context |
| `FORGE_IMAGE_PREFIX` | `exe-image-forge` | Local base/dev image namespace |
| `FORGE_VM_NAME` | current hostname | exe.dev VM name used in commands |
| `FORGE_CONFIG` | `/etc/exe-image-forge/config.json` | Hashed password and vending config |
| `FORGE_AUTH_HOME` | `/var/lib/exe-image-forge/authhome` | Persistent CLI credentials |
| `FORGE_REGISTRY_DATA` | `/var/lib/exe-image-forge/registry` | Registry blob storage |

The password is intentionally not an environment variable. Change it with:

```bash
exe-image-forge password
```

## Image variants

| Variant | Extra tools |
| --- | --- |
| `min` | none |
| `gemini` | Gemini CLI |
| `go` | Go toolchain |
| `go-gemini` | Go toolchain and Gemini CLI |

Optional components are the final layers, so all variants share the large
Codex and Claude layers. Build one variant during development with
`exe-image-forge build go`, or all variants with `exe-image-forge build`.

## Common commands

```text
exe-image-forge build [--fresh] [variant]
exe-image-forge auth {gh|codex|claude|gemini|all}
exe-image-forge status
exe-image-forge bake [variant]
exe-image-forge verify [image]
exe-image-forge sizes
exe-image-forge versions [image]
exe-image-forge grants
exe-image-forge gc
```

Run `exe-image-forge help` for the complete list.

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

Each grant adds a metadata-only layer, creating a unique digest and tag without
duplicating the parent image layers. Expiration removes the tag and route;
scheduled registry garbage collection later reclaims unreferenced blobs.

## Development

```bash
make check
```

This runs Go tests, `go vet`, formatting checks, and Bash syntax checks. CI runs
the same validation on pushes and pull requests.

The detailed original Korean operations guide remains available at
[`docs/README.ko.md`](docs/README.ko.md).

## License

Apache-2.0. The image is derived in part from
[boldsoftware/exeuntu](https://github.com/boldsoftware/exeuntu); see
[`NOTICE`](NOTICE).
