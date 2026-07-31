# Contributing

Issues and pull requests are welcome. Keep changes focused and include tests for
security boundaries, registry routing, credential selection, or authentication
behavior.

Before opening a pull request:

```bash
make check
```

Never commit live `forge.env`, `config.json`, authentication homes, registry
data, generated grants, exported credentials, or credentialed Docker archives.
