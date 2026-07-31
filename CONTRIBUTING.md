# Contributing

Issues and pull requests are welcome. Keep changes focused and include tests for
security boundaries, registry routing, credential selection, or authentication
behavior.

Before opening a pull request:

```bash
make check
```

For web behavior or layout changes, also run:

```bash
make dev
make e2e
```

The demo server is loopback-only, uses password `forge-demo`, and contains
fixtures rather than provider credentials. See
[`docs/ui.md`](docs/ui.md) and [`docs/api.md`](docs/api.md) before changing the
browser contract.

Never commit live `forge.env`, `config.json`, authentication homes, registry
data, generated grants, exported credentials, or credentialed Docker archives.
