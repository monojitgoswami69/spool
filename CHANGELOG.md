# Changelog

## v0.4.1

### Honest status & live progress
- `spool init/up/restart/heal/update` now stream live build progress (BuildKit
  steps) and per-service health-check transitions instead of going silent
  during the build/verify phase.
- Final status waits for health checks to settle before rendering, and live
  endpoints are only printed for containers that are actually running — a
  stopped stack no longer advertises URLs (`spool status` included).
- `/api/health` reports the build-injected version instead of a hardcoded one.

### Web console
- Greetings and small talk are answered deterministically — no LLM, no tools.
- Dynamic streaming states ("Thinking…", "Checking logs and health…", …) while
  the agent works, and a typewriter reveal for finished replies.
- Intent tags hidden; nerd stats moved to hover-only; tool pills no longer
  dump raw JSON args.
- Project selector addresses projects by unique id, so two projects sharing a
  name (same repo onboarded twice) stay separately selectable.

### Interactive environment editor
- New `spool env <project> config`: an interactive panel to edit, add, unset,
  and reveal project variables (secrets masked), persisting as you type and
  applying to running stacks on exit.

## v0.4.0

### Web control plane
- `spool serve`: local chat-first web UI with project sidebar, persisted chats
  in `~/.spool/chats`, streaming SSE agent events, slash commands for all
  operations, confirmation modals, and a nerd-stats drawer.
- Nerd stats: intent, tool calls, args, outputs, durations, bytes, raw events.

### Agent-first repo intelligence
- Scanner collects README, docs, Makefile, scripts, existing Dockerfiles and
  compose files; exposes them through `inspect_repository`.
- Existing Dockerfiles are reused by default; if the reused build fails, Spool
  regenerates managed Dockerfiles and retries automatically.
- Agent system prompt now explicitly prefers inspecting/reusing repo assets.

### Interactive config
- `spool config` and `spool config set` (no args) open a proper terminal
  config panel: provider cycling, editable model/base URL, masked API key and
  GitHub PAT with reveal, and save. `config set KEY VALUE` still works for
  scripts.

### Bulletproofing
- Strict compose validation, rollback of failed patches, source-error detection,
  real BuildKit error extraction, and fast crash-loop failure.
- Node 22 default / Node 24 for `node:sqlite`; Dockerfile-only and static repos;
  nested-app safe build contexts.


## v0.3.0

### Agent-first
- Agent-native onboarding: `onboard_repository` tool + `onboard` intent. From a
  clean slate, the agent clones/scans/generates/runs/heals/verifies a repo and
  returns live URLs. Chat auto-switches context to the new project.
- New `diagnose` intent + `diagnose_failure` tool: explains what failed, why,
  with evidence, container state, and next steps.
- Persisted `last-failure.log` so diagnosis works after failed builds.
- LLM healer now rolls back experimental patches when it cannot fix a repo.
- Strict compose validation (YAML, duplicate keys, service removal) with
  validation feedback retried by the model.
- Real error extraction from BuildKit output, including TypeScript/Next errors.

### Compatibility
- Node 22 default; auto Node 24 when `node:sqlite` is detected.
- Dockerfile-only repos are supported (existing Dockerfile reused).
- Static root sites are detected alongside nested backends.
- Nested independent Node apps build from their own contexts; root builds
  exclude them via a managed `.dockerignore` block.
- Cookiecutter/template trees are skipped instead of misdetected.
- Missing env vars are reported after init/update with an exact set command.

### Experience
- Modern chat TUI: centered layout, bottom-pinned rounded input, spinner,
  intent badges, streaming tool pills, confirmation boxes.
- Interactive `spool setup` wizard with live LLM verification.
- Interactive `spool env` prompts for missing variables (secrets masked).

### Tests
- Added live heal proof (runtime repair, build repair, honest failure rollback).
- Added 8-repo and 23-repo real-repo matrices and public-internet validation.
- Added scanner/generator/healer/chat tests for all new paths.

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

### Healer hardening (live-verified)
- LLM plans that target generic `Dockerfile` are normalized to the failing
  service's actual filename.
- Validation failures are fed back to the model and retried instead of aborting
  the heal loop.
- Runtime crash loops (`restarting` with no exit code) fail fast after a grace
  period instead of waiting the full health timeout.
- Entry files (`main.py`, `app.py`, `server.js`, ...) are included in the LLM
  repair context.
- `make heal-proof` proves a live runtime repair and a live build repair.

### Fixes
- Port allocator now detects Docker's `0.0.0.0` bindings (3000→3001 remap).
- `spool down` no longer panics from a `-v` flag collision.
- Non-interactive destructive intents require `--yes`.
- Env changes only recreate stacks that are already running.
