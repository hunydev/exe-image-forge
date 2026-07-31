# HTTP and WebSocket API

This document describes the browser-facing contract of the Exe Image Forge
vending service. It is intended for contributors and automation; registry
clients should use the image URL returned by `POST /api/grant`.

## Conventions

- Browser APIs are same-origin and use JSON unless noted otherwise.
- Errors are plain UTF-8 text with an appropriate HTTP status.
- Request bodies are limited by the service; clients should not send unknown
  large fields.
- Protected endpoints use the `exe_image_forge_admin` session cookie. The
  cookie is HttpOnly, Secure, SameSite=Lax, and valid for eight hours.
- Session, credential, and inventory responses use `Cache-Control: no-store`.
- Timestamps use RFC 3339 UTC strings.
- Production deployments are expected to run behind the documented exe.dev
  HTTPS proxy. The local service binds to loopback.

## Public and session endpoints

### `GET /healthz`

Local readiness check.

```text
ok
```

### `GET /api/session`

Returns the current browser session without performing credential or Docker
inspection.

Unauthenticated:

```json
{
  "authed": false,
  "passkeys": 0
}
```

Authenticated:

```json
{
  "authed": true,
  "passkeys": 1,
  "expires": "2026-01-15T20:00:00Z"
}
```

`passkeys` counts only passkeys usable for the request's current relying-party
domain.

### `POST /admin/api/login`

Password login.

```json
{
  "password": "user-supplied password"
}
```

Success returns `200` and establishes the session:

```json
{
  "ok": true
}
```

Errors include:

| Status | Meaning |
| --- | --- |
| `400` | Invalid JSON |
| `403` | Wrong password |
| `405` | Method is not `POST` |
| `429` | Per-client authentication limiter is active |

A `429` response includes `Retry-After`.

### `POST /admin/api/logout`

Deletes the current server-side session and expires its cookie.

```json
{
  "ok": true
}
```

### `GET /api/info`

Returns configured repositories, variant availability, and default TTL.
Grant detail is added only for a direct loopback request; proxied clients
receive the aggregate count.

```json
{
  "repos": [
    {
      "name": "exe-image-forge/dev",
      "label": "exe-image-forge/dev (signed in)",
      "note": "Includes Codex, Claude, Gemini, and GitHub credentials",
      "baked": true
    }
  ],
  "pull_host": "images.example.com",
  "ttl_minutes": 30,
  "active_count": 0,
  "variant_names": ["core", "codex", "claude", "min"],
  "variants": {
    "min": {
      "built": true,
      "bytes": 417333248,
      "size": "398MB"
    }
  },
  "demo": false
}
```

The real response lists all 16 variant names. `bytes` and `size` are omitted
when a variant has not been built.

### `GET /api/creds`

Unauthenticated responses contain aggregate state only:

```json
{
  "authed": false,
  "counts": {
    "ok": 3,
    "missing": 1
  },
  "warnings": ["Not signed in: Gemini CLI"],
  "baked": "2026-01-15T12:00:00Z",
  "passkeys": 0
}
```

Authenticated responses add credential details and tool versions:

```json
{
  "authed": true,
  "creds": [
    {
      "tool": "codex",
      "name": "Codex CLI",
      "file": ".codex/auth.json",
      "state": "ok",
      "expires": "2026-01-29T12:00:00Z",
      "seconds_left": 1209600,
      "refreshable": true,
      "detail": "ChatGPT sign-in",
      "login_cmd": "codex login --device-auth",
      "needs_relay": false
    }
  ],
  "warnings": [],
  "baked": "2026-01-15T12:00:00Z",
  "authed_home": "/var/lib/exe-image-forge/authhome",
  "passkeys": 0,
  "passkeys_total": 0,
  "terminal_mode": "auth-host",
  "versions": {
    "codex": "0.110.0",
    "updated": "2026-01-15T12:00:00Z"
  }
}
```

Credential `state` is one of `missing`, `ok`, `stale`, `expired`, or
`unknown`. Account names, credential paths, and login commands must never be
added to the unauthenticated form.

`terminal_mode` is returned only to an authenticated session:

- `container` opens the full generated image and accepts terminal input.
- `auth-host` requires a `tool` selector and starts an exact allowlisted login
  command on the appliance host. It never interprets browser input as a shell
  command.

## Image grants

### `POST /api/grant`

Requires an active session. A legacy client may instead include `password`,
but new browser clients should rely on the session.

```json
{
  "repo": "exe-image-forge/dev",
  "ttl": 30,
  "with_codex": true,
  "with_claude": true,
  "with_gemini": false,
  "with_go": false
}
```

`with_codex` and `with_claude` default to `true` when omitted for compatibility.
`ttl` defaults to the configured TTL, has a minimum effective value of one
minute through the UI, and is clamped to 1,440 minutes by the service.

Success:

```json
{
  "repo": "exe-image-forge/dev",
  "variant": "min",
  "tag": "<grant-tag>",
  "image": "images.example.com/t/<grant-token>/exe-image-forge/dev:<grant-tag>",
  "token": "<grant-token>",
  "expires": "2026-01-15T12:30:00Z",
  "ttl_minutes": 30,
  "exe_cmd": "ssh exe.dev new --image=...",
  "docker_cmd": "docker pull ..."
}
```

The values above are structural examples, not usable credentials.

| Status | Meaning |
| --- | --- |
| `400` | Invalid JSON or unknown repository |
| `403` | No valid session and wrong or missing legacy password |
| `405` | Method is not `POST` |
| `429` | Authentication limiter is active |
| `500` | Disk guard, image publication, or state persistence failed |

### `/v2/t/<token>/<repo>/...`

Read-only registry proxy for a single grant.

- `GET` and `HEAD` are accepted.
- The path must remain inside the repository recorded in the grant.
- Unknown, expired, or cross-repository tokens return a registry-style denial.
- Authorization headers from callers are removed before proxying.
- The unscoped `/v2/` ping returns the Docker Registry API version and no
  protected data.

## Authentication terminal

### `GET /admin/api/term`

Requires an active session and upgrades to a WebSocket. `cols` and `rows`
provide the initial PTY size.

In `auth-host` mode, `tool` is required and must be one of `gh`, `codex`,
`claude`, or `gemini`. A missing or unknown value returns `400` before a
WebSocket is accepted. The value selects a server-side fixed command; it is not
an executable name or shell fragment.

Text or binary frames after connection are PTY input. A text frame beginning
with a NUL byte contains a JSON resize control:

```json
{
  "cols": 120,
  "rows": 32
}
```

## Protected administration endpoints

All endpoints in this section return `401` without an active session unless
explicitly marked as a passkey login endpoint.

### `GET /admin/api/context?variant=<name>`

```json
{
  "variant": "codex-go",
  "context": "# Generated agent context\n..."
}
```

An invalid or unbuilt variant returns an empty `context`.

### `POST /admin/api/bake`

Starts one asynchronous credential bake. A concurrent request returns `409`.

```json
{
  "started": true
}
```

### `GET /admin/api/bake-status`

```json
{
  "running": false,
  "log": ["bake output line"],
  "error": "",
  "at": "2026-01-15T12:00:00Z",
  "baked": "2026-01-15T12:00:00Z"
}
```

Only the most recent bounded log tail is returned.

### `GET /admin/api/term`

WebSocket endpoint connected to a single PTY in the fullest base image.
Only one terminal may run at a time. Browser-to-server text/binary messages are
terminal input. A message beginning with a NUL byte contains resize metadata:

```json
{
  "cols": 120,
  "rows": 36
}
```

Demo mode returns a non-executing echo terminal instead of starting Docker.

### `POST /admin/api/relay`

Fallback for a Gemini localhost OAuth callback.

```json
{
  "url": "http://localhost:52341/oauth2callback?code=...&state=..."
}
```

Only `localhost`, `127.0.0.1`, non-privileged ports, and the expected callback
path are accepted.

### Passkey endpoints

| Endpoint | Authentication | Purpose |
| --- | --- | --- |
| `POST /admin/api/passkey/login/begin` | None | Start discoverable WebAuthn login |
| `POST /admin/api/passkey/login/finish` | None | Verify assertion and create a session |
| `POST /admin/api/passkey/register/begin` | Session | Start passkey registration |
| `POST /admin/api/passkey/register/finish?label=<name>` | Session | Verify and store the credential |
| `GET /admin/api/passkey/list` | Session | List passkey metadata for all domains |
| `POST /admin/api/passkey/delete` | Session | Delete by `{ "id": "...", "rpid": "..." }` |

The begin/finish payloads follow WebAuthn browser structures and are handled by
`vend/passkey.js`. New clients should reuse that implementation rather than
constructing ceremony payloads manually.

## Demo mode

`make dev` starts deterministic fixture data at
`http://127.0.0.1:18080` with password `forge-demo`.

Demo mode:

- refuses non-loopback listen addresses
- never reads real credentials
- never builds or pushes an image
- provides all 16 variants and four healthy credential fixtures
- simulates grant publication and baking
- marks API inventory with `"demo": true`

It is a development aid, not an alternative deployment mode.
