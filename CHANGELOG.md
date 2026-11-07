# Changelog

## v0.2.0

### Highlights
- **Intent-aware agent**: deterministic parser + validated LLM classifier,
  clarification questions, direct read-only execution, and `spool ask`.
- **New commands**: `ask`, `env`, `config`, `restart`, `open`, `stats`,
  `update`, `list` alias, shell completion.
- **Zero-config env import**: repository `.env` files are imported; secrets are
  masked in listings and written 0600.
- **Broader detection**: static sites, hapi/restify/pola/sails/loopback-style
  Node servers, source-declared ports, `.python-version`, `requirements`
  include cycles, and dependency-only `pyproject.toml`.
- **Hermetic builds**: public `NEXT_PUBLIC_*`/`VITE_*` build args, no
  unconditional PyTorch index, relative compose contexts, app healthchecks.
- **Safety**: symlink-resolved repo jail, 0600 secrets, port probing on all
  interfaces, destructive-action confirmation, orphan cleanup.
- **Release engineering**: GitHub Actions CI, GoReleaser, `install.sh`,
  end-to-end no-Docker pipeline tests, race-tested suite.

### Fixes
- Port allocator now detects Docker's `0.0.0.0` bindings (3000→3001 remap).
- `spool down` no longer panics from a `-v` flag collision.
- Non-interactive destructive intents require `--yes`.
- Env changes only recreate stacks that are already running.
