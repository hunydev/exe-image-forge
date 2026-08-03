# Exe Image Forge appliance

The appliance is the repository's bootable, self-hosted distribution. A GitHub
release publishes it to:

```text
ghcr.io/hunydev/exe-image-forge:<release>
ghcr.io/hunydev/exe-image-forge:latest
```

It is distinct from the images created by the forge:

| Image | Purpose | Contains provider credentials |
| --- | --- | --- |
| GHCR appliance | Boots the forge web service and build system | Never |
| Local `exe-image-forge/base:*` | Starts new logged-out development VMs | Never |
| Local `exe-image-forge/dev:*` | Starts pre-authenticated development VMs | Yes |

## Create a VM

```bash
ssh exe.dev new \
  --name=image-forge \
  --cpu=4 \
  --memory=8GB \
  --disk=40GB \
  --image=ghcr.io/hunydev/exe-image-forge:latest
```

The image carries exe.dev's `login-user` and `install-shelley` OCI labels. It
also exposes port 8000. The documented exe.dev HTTPS proxy therefore selects
the web service automatically, without a setup script or a separate
`share port` command.

The proxy is private by default. Keep it private unless public access is an
explicit operational requirement.

For a private GHCR fork, supply a classic GitHub token with `read:packages`:

```bash
ssh exe.dev new \
  --image=ghcr.io/OWNER/exe-image-forge:latest \
  --registry-auth=USERNAME:TOKEN
```

## First boot

Run:

```bash
ssh image-forge.exe.xyz exe-image-forge-first-login
```

The command prints the VM URL, its randomly generated initial password, and
the relevant service checks. The password is generated on that VM and stored
at `/var/lib/exe-image-forge/initial-password` with mode `0600`; it is never
part of an image layer or GitHub secret.

Change it with:

```bash
ssh image-forge.exe.xyz exe-image-forge password
```

The password command updates the salted hash and removes the plaintext initial
password file.

## Boot sequence

Systemd starts these components:

1. `exe-image-forge-initialize.service` creates host-specific configuration,
   persistent directories, and the first-boot password.
2. Docker and containerd start.
3. `exe-image-forge-vend.service` serves the web on `127.0.0.1:8000`.
4. `exe-image-forge-registry.service` creates or starts the registry container
   on `127.0.0.1:5000`.
5. `exe-image-forge-bootstrap.service` builds all 16 logged-out base variants
   at reduced CPU and I/O priority.
6. Existing weekly refresh and daily garbage-collection timers take over.

The web and registry jobs are independent, so the login UI can become ready
while the registry image is pulled. Bootstrap waits for both and does not block
web readiness.

Check them with:

```bash
systemctl status exe-image-forge-vend.service --no-pager
systemctl status exe-image-forge-registry.service --no-pager
systemctl status exe-image-forge-bootstrap.service --no-pager
curl -fsS http://127.0.0.1:8000/healthz
```

Follow the initial build:

```bash
journalctl -u exe-image-forge-bootstrap.service -f
```

To defer it, include `--env FORGE_AUTO_BUILD=0` when creating the VM. Run
`exe-image-forge build` over SSH when ready.

## Authentication terminal

The appliance sets `FORGE_TERMINAL_MODE=auth-host`. In this mode the browser
sends only an opaque provider identifier. The server maps it to one fixed
command:

| Provider | Fixed command |
| --- | --- |
| GitHub | `gh auth login --git-protocol https` |
| Codex | `codex login --device-auth` |
| Claude | `claude auth login` |
| Gemini | `NO_BROWSER=true gemini` |
| Cloudflare | `wrangler login --no-use-keyring` |

No browser-supplied shell text, executable, or argument is accepted.
Credentials are written to `/var/lib/exe-image-forge/authhome` and can later be
baked through the existing allowlist.

Wrangler's OAuth provider returns to `http://localhost:8976`. On a remote VM,
leave the terminal waiting, copy the complete failed browser URL, and paste it
into the **OAuth callback relay** below the terminal. The relay permits only
loopback callback addresses and sends the request from the appliance.

## Persistent and replaceable data

Preserve these paths when upgrading or recovering a VM:

- `/etc/exe-image-forge/config.json`
- `/etc/exe-image-forge/forge.env`
- `/var/lib/exe-image-forge/authhome`
- `/var/lib/exe-image-forge/registry`
- `/var/lib/exe-image-forge/grants.json`

The source tree and vending binary under `/opt/exe-image-forge` come from the
appliance image and are replaceable. First-boot initialization is idempotent:
an existing config and environment file are never overwritten.

## Release supply chain

Publishing a GitHub release runs `.github/workflows/release.yml`. It builds
Linux AMD64 and ARM64 images, pushes the release tag and (for a non-prerelease)
`latest`, attaches SBOM and provenance attestations, and records the immutable
manifest digest in the workflow summary.

After a successful release, `.github/workflows/appliance-smoke.yml` pulls the
public image and boots it as a privileged PID 1/systemd container. The smoke
test checks first-boot initialization, Docker-backed registry startup, port
8000 health, Wrangler availability, restricted authentication mode, and the absence of protected
credential metadata from unauthenticated responses.

The appliance is built directly on a pinned Ubuntu 24.04 multi-platform
manifest and carries only Forge's boot/runtime dependencies. The previous
general-purpose exeuntu base transferred 1.54 GiB for AMD64, including tools
that the appliance never used. Releases now enforce a 768 MiB compressed budget
for both AMD64 and ARM64; generated development images remain separate.
