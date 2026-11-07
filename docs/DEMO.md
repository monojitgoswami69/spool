# Spool — Judge Demo Runbook

A 4–5 minute, from-scratch demonstration. All commands use the installed `spool`
binary (fish/zsh/bash). Docker Desktop must be running.

## Pre-flight (10s)

```bash
docker info >/dev/null && echo "Docker ready"
spool --version
spool doctor
spool status          # clean slate: "No projects yet"
```

Talking point: *“One binary, zero state, five provider integrations, and a
deterministic-first intent engine.”*

## Act 1 — Product surface (30s)

```bash
spool --help
spool config show      # provider/model/key source/paths, key masked
spool doctor           # git, Docker, Compose v2, LLM, tunnel tooling
```

## Act 2 — From scratch: first app (~2 min; narrate while it builds)

```bash
spool init /Users/monojitgoswami/projects/certify-v2
```

Call out the 8 steps:
1. resolve source → 2. scan services → 3. discover env → 4. infer infra →
5. generate manifests → 6. persist state → 7. build + heal + verify → 8. healthy.

Key points:
- Detects **Next.js** automatically and imports the repo’s real `.env`.
- Detects **Neon Postgres** and marks it **external** — no duplicate local DB.
- Health is verified with a real HTTP probe (never discarded).
- Live URL: **http://localhost:3000**

## Act 3 — Second app + port intelligence (~30s)

```bash
spool init /Users/monojitgoswami/projects/portfolio-website --max-services 1
```

- Port 3000 is taken, so Spool automatically remaps to **3001**.
- `.env` values (Gemini, Upstash) are imported; **Upstash Redis** is external.
- Live URL: **http://localhost:3001**

Open both URLs side-by-side.

## Act 4 — Intent-aware agent (60s)

```bash
spool ask "what is running?"
spool ask "explain the topology"
spool ask "show environment for portfolio-website"
spool ask "check the health"
spool ask "set GEMINI_MODEL=gemini-3.5-flash-lite on portfolio-website"
spool ask "why did it fail?"          # diagnosis with evidence + next steps
spool ask --yes "onboard https://github.com/vercel/nextjs-portfolio-starter"
spool --yes ask "restart portfolio-website"
```

Every request prints its parsed intent first (`🎯 ...`). Ambiguous requests ask
one clarifying question and execute nothing.

Then the interactive REPL:

```bash
spool chat
```

```text
diagnose
why is the api unhealthy?
logs for web
use project certify-v2
heal
```

## Act 4.5 — Web control plane (60s)

```bash
spool serve
```

Open the printed URL. Show:
- chat sidebar with persisted conversations
- project selector with live running/healthy counts
- streaming tool pills and intent chips
- `/onboard <url>` with a confirmation modal
- **Nerd stats** drawer: tool args, outputs, durations, bytes, raw events

## Act 5 — Show the engine room (60s)

```bash
spool status
spool explain certify-v2

set P (ls -d ~/.spool/projects/certify-v2-* | head -1)   # fish
ls -la $P
sed -E 's/=.*/=<masked>/' $P/.env | head
cat $P/docker-compose.yml
cat $P/Dockerfile.certify-next
```

Call out:
- Portable relative build context + Dockerfile.
- Infra host-port mapping vs internal container ports (`5439:5432` style).
- 0600 `.env`, lock file, repairs.json provenance (SHA-256).
- `.dockerignore` is generated automatically.

```bash
spool stats certify-v2
spool logs certify-v2
spool dash portfolio-website
```

## Act 6 — Reset / teardown (20s)

```bash
spool down certify-v2
spool --yes nuke portfolio-website
```

`nuke` removes containers, volumes, state, and (for remote clones) the clone,
guarded so it only deletes inside Spool-owned roots.

## Reset to a clean slate

```bash
spool --yes nuke certify-v2
spool --yes nuke portfolio-website
spool status          # No projects yet
```

## Fallback if a build is slow or fails

```bash
spool heal certify-v2          # deterministic rules first, then guarded LLM repair
spool logs certify-v2          # inspect the failure
```

## Judge talking points

- **Dynamic port allocation** for apps *and* infra; 3000→3001 handled live.
- **Cloud suppression**: Neon/Upstash external, not duplicated locally.
- **Real verification**: HTTP + container health, no false success.
- **Intent-first agent**: deterministic parser → validated LLM classifier →
  clarify → confirm → act. Read-only intents need no LLM.
- **Security**: symlink-resolved repo jail, 0600 secrets, PAT via env (not argv),
  invariant-guarded patches, SHA-256 provenance.
- **Determinism**: sorted ports/graph/env; compose validated before running.
- **Zero-config env**: repo `.env` imported; `spool env` shows/edits live.