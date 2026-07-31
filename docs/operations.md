# Exe Image Forge operations

Exe Image Forge builds exe.dev-compatible custom images and issues
password/passkey-protected image paths that remain valid for a limited time.

## Architecture

```text
browser ──HTTPS──> exe.dev proxy ──> :8000 vending service
                                          ├── /                 image grants
                                          ├── /admin/           administration
                                          ├── /api/grant        temporary grant
                                          └── /v2/t/<token>/…   scoped registry proxy
                                                     │
                                                     v
                                           :5000 registry:2
                                           bound to localhost
```

- `exe-image-forge/base` contains tools without credentials.
- `exe-image-forge/dev` adds allowlisted, pre-authenticated credentials.
- Each grant creates a metadata-only image layer and a unique tag.
- Expiry removes the tag and token route. A six-hour reconciliation pass also
  removes old, Forge-labeled tags that are not present in grant state. The
  daily garbage-collection timer later reclaims unreferenced registry blobs.

## Browser workflow

Both pages use the same eight-hour password/passkey session. Image selection,
TTL, component options, and grant controls remain hidden until authentication
succeeds. A lightweight session check runs every second, while
`BroadcastChannel` propagates login/logout changes immediately between open
tabs. Expiry or logout hides protected controls and returns the browser to the
sign-in screen.

The admin console is organized into four tabs:

- **Overview** — credential health, built-variant inventory, tool versions, and
  the most recent bake.
- **CLI Logins** — credential details and the browser terminal used for OAuth
  and device-code flows.
- **Images** — credential bake workflow and per-variant agent-context preview.
- **Security** — passkey registration, usage, and removal.

Unauthenticated callers never receive account names, credential paths, terminal
access, bake controls, or image-grant controls.

## CLI authentication

The browser terminal is a real PTY running in the full
`base:go-gemini` image with the persistent authentication home mounted. Only
one terminal may run at a time to prevent credential-file races.

| CLI | Command | Flow |
| --- | --- | --- |
| GitHub CLI | `gh auth login --git-protocol https` | device code |
| Codex CLI | `codex login --device-auth` | device code |
| Claude Code | `claude auth login` | redirect and pasted code |
| Gemini CLI | `NO_BROWSER=true gemini` | redirect and pasted code |

Do not use `claude setup-token` for image authentication. It prints a token for
an environment variable but does not persist the credential file that the bake
workflow needs.

`NO_BROWSER=true` normally keeps Gemini away from a localhost callback. The
admin relay is retained as a fallback for a CLI version that still redirects to
`http://localhost:<port>/oauth2callback?...`. It accepts only localhost or
127.0.0.1 on non-privileged ports, preventing the endpoint from becoming an
authenticated SSRF primitive.

## CLI workflow

```sh
exe-image-forge build
exe-image-forge auth all
exe-image-forge status
exe-image-forge bake
exe-image-forge verify
```

Credentials persist in `/var/lib/exe-image-forge/authhome` by default. Rebuilding
base images does not remove them; they enter a dev image only during `bake`.

If credentials already exist on another machine:

```sh
exe-image-forge export-hint
```

The command prints an allowlisted tar workflow for importing them.

## Command reference

| Command | Purpose |
| --- | --- |
| `exe-image-forge build [--fresh] [variant]` | Build all base variants or one variant |
| `exe-image-forge auth {gh\|codex\|claude\|gemini\|all}` | Authenticate inside the full image |
| `exe-image-forge import [file]` | Import a credential tar, stdin by default |
| `exe-image-forge export-hint` | Print the credential export command |
| `exe-image-forge shell` | Open an interactive shell with persistent HOME |
| `exe-image-forge status` | Inspect credential availability |
| `exe-image-forge bake [variant]` | Build credentialed dev images |
| `exe-image-forge verify [image]` | Check tools and authentication |
| `exe-image-forge sizes` | Show image sizes |
| `exe-image-forge versions [image]` | Show installed tool versions |
| `exe-image-forge context [variant]` | Preview generated agent instructions |
| `exe-image-forge password` | Change the web password |
| `exe-image-forge token [token]` | Store the exe.dev VM token used by Docker |
| `exe-image-forge vend-build` | Rebuild and restart the vending service |
| `exe-image-forge grants` | List active grants |
| `exe-image-forge gc [--dry-run]` | Safely prune local, build-cache, and registry garbage |

## Image variants

GitHub CLI, Node.js, Python, uv, Git, and build-essential are always present.
Codex, Claude, Gemini, and Go are independently selectable.

| Codex/Claude selection | Tag base |
| --- | --- |
| neither | `core` |
| Codex only | `codex` |
| Claude only | `claude` |
| both | `min` |

Gemini and Go add `-gemini`, `-go`, or `-go-gemini`. The historical tags
`gemini`, `go`, and `go-gemini` retain their original meaning: Codex and Claude
plus those components. This produces 16 combinations.

Codex and Claude remain selected by default in the web UI for backward
compatibility. An omitted optional CLI is not silently reinstalled by the
in-image update timer.

Current reference sizes with zstd level 9:

| Variant | Compressed size |
| --- | ---: |
| `core` | 241 MB |
| `codex` | 330 MB |
| `claude` | 309 MB |
| `min` | 398 MB |
| `go-gemini` | 463 MB |

Actual sizes change as upstream tools release new versions. Use
`exe-image-forge sizes` for the authoritative local values.

## Base versus dev

| Repository | Contents |
| --- | --- |
| `exe-image-forge/dev` | tools plus authenticated credentials |
| `exe-image-forge/base` | tools only; input for the bake workflow |

Use the dev repository when the new VM must arrive already authenticated.
Receiving a logged-out base image is intentional, not a failed bake.

After baking, the CLI verifies every credential whose tool exists in the
selected variant. Omitted tools are skipped rather than reported as failures.

## Credential allowlist

The authentication home may also contain prompt logs, chat transcripts,
SQLite state, shell history, and project metadata. None of those files belong
in a distributed image.

The bake workflow therefore copies only entries from `CRED_FILES`; it never
archives the entire authentication home with a denylist. It prints the exact
file list for every bake. Context files such as `AGENTS.md` are also excluded
so one variant cannot overwrite another variant's generated instructions.

Gemini CLI may encrypt credentials using a key derived from the hostname and
username. Before baking, `image/files/gemini-export-creds.js` converts a
credential on the login host into a portable OAuth file or an image
`GEMINI_API_KEY` environment value.

## Agent context

At boot, `agent-context.service` writes current machine and tool information to:

| CLI | Global instruction file |
| --- | --- |
| Codex | `~/.codex/AGENTS.md` |
| Claude | `~/.claude/CLAUDE.md` |
| Gemini | `~/.gemini/GEMINI.md` |

The generated block includes OS, kernel, CPU, RAM, disk, installed tool
versions, exe.dev HTTPS behavior, authentication availability, and updater
behavior. It explicitly lists omitted optional tools. Marker-delimited updates
preserve user-authored content elsewhere in the same files.

## Updates

There are two update layers:

- Inside a created VM, `ai-cli-update.timer` runs shortly after boot and daily,
  with jitter to avoid release-server spikes.
- On the forge VM, `exe-image-forge-refresh.timer` rebuilds base images weekly
  and re-bakes dev images when credentials exist.

Each tool updates independently. A failed network request for one CLI does not
block the others. New binaries are version-checked before replacement, and the
result is written to `/etc/ai-cli-versions.json`.

## HTTPS and registry access

Point the documented exe.dev proxy at the vending service:

```sh
ssh exe.dev share port <forge-vm-name> 8000
```

The proxy remains private by default. Make it public only when deliberately
required:

```sh
ssh exe.dev share set-public <forge-vm-name>
```

The backing registry binds only to `127.0.0.1:5000`. External reads go through
the read-only `/v2/t/<token>/…` route, limited to the granted repository and
GET/HEAD methods.

## Security model

- The web password is stored as PBKDF2-HMAC-SHA256 with a random salt and
  210,000 iterations.
- Five failed attempts start an increasing per-client lockout shared by
  password and passkey login. Password hashing is globally concurrency-bounded
  and does not hold the grant/pull lock.
- Browser sessions use HttpOnly, Secure, SameSite=Lax cookies and expire after
  eight hours.
- WebAuthn derives and validates the relying-party ID from the request host.
  Stored signature counters detect cloned authenticators.
- Terminal, bake, passkey management, credential detail, and image grants
  require an active session.
- Grant tokens are 128-bit random values, read-only, scoped to one repository,
  and limited to a maximum 24-hour TTL.
- The authenticated terminal loads its pinned xterm distributions locally.
  CSP hashes allow only the repository's inline application code, while common
  security headers block framing, MIME sniffing, and unnecessary browser APIs.

## Registry maintenance and disk guard

Registry garbage collection is stop-the-world. The command takes the same file
lock used by grant publication, stops the registry, runs a one-shot collector
against its volumes, and restarts the registry even when collection fails or
the process receives a termination signal. `--dry-run` does not prune local
images or build cache and does not delete registry data.

Grant publication, base builds, and credential bakes refuse to start when disk
usage reaches `FORGE_MAX_DISK_PERCENT` or free space falls below
`FORGE_MIN_FREE_BYTES`. GC removes dangling images and Forge-labeled local
grant copies, then bounds Buildx cache with `FORGE_BUILD_CACHE_MIN_FREE` and
`FORGE_BUILD_CACHE_RESERVED`.

Orphan reconciliation only considers configured repositories and 16-character
grant tags. A tag must carry a matching Forge grant label, be absent from active
state, and be older than `FORGE_ORPHAN_GRACE` before it can be removed.

## Host paths

Defaults:

- checkout: installation directory selected by `install.sh`
- config: `/etc/exe-image-forge/config.json`
- grants: `/var/lib/exe-image-forge/grants.json`
- authentication home: `/var/lib/exe-image-forge/authhome`
- registry data: `/var/lib/exe-image-forge/registry`
- service: `exe-image-forge-vend.service`
- timers: `exe-image-forge-refresh.timer`, `exe-image-forge-gc.timer`
