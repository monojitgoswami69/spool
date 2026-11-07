# Security

## Secrets
- `.env`, `.env.*`, and the `spool` binary are gitignored; only `.env.example`
  is committed.
- Spool writes project env files with `0600` permissions and refuses
  group/other-writable files.
- Credential-bearing URLs (e.g. `postgres://user:pass@host`) are masked in
  `spool env`.
- Git tokens are passed through `GIT_CONFIG_*` environment variables, never
  argv, and are scrubbed from errors.

## Filesystem boundary
- All agent file reads and writes go through `safefs.Jail`, which resolves
  symlinks on the longest existing prefix, rejects `..` and absolute escapes,
  and caps reads.
- The healer may only modify generated `Dockerfile.*`, `docker-compose.yml`,
  and `.dockerignore` inside the project workspace.
- `nuke` only removes paths inside Spool-owned roots and refuses `/`, `.`, and
  the home directory.

## Repair invariants
LLM patches are rejected when they would disable type checking, remove a
healthcheck, replace a command with `sleep`, drop install/build steps, use
absolute compose paths, or rewrite beyond a bounded scope. Applied patches are
recorded with before/after SHA-256 in `repairs.json`.

## Reporting
Open a private security advisory at
https://github.com/monojitgoswami69/spool/security/advisories/new.
