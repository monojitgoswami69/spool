#!/usr/bin/env bash
# Live proof that the LLM healer repairs real failures (requires Docker + LLM key).
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
BIN=${BIN:-$ROOT/spool}
export SPOOL_HOME=${SPOOL_HOME:-/tmp/spool-heal-home}

rm -rf "$SPOOL_HOME"
mkdir -p "$SPOOL_HOME"

# Remove containers from previous runs (same deterministic project ids).
docker ps -aq --filter "name=llm-runtime-heal" | xargs -r docker rm -f >/dev/null 2>&1 || true
docker ps -aq --filter "name=llm-build-heal"   | xargs -r docker rm -f >/dev/null 2>&1 || true

fail=0

run_case() {
  name=$1
  expect=$2
  log="/tmp/heal-$name.log"
  echo
  echo "=============================================================="
  echo "CASE: $name"
  echo "=============================================================="
  if ! timeout 600 "$BIN" --no-color init "$ROOT/test/fixtures/$name" >"$log" 2>&1; then
    echo "FAIL: init exited non-zero"
    tail -25 "$log"
    fail=1
    return
  fi

  id=$(grep -oE 'project [^ ]+ · state' "$log" | head -1 | awk '{print $2}')
  proj=$(grep -oE 'manifest\(s\) in .*' "$log" | head -1 | sed 's/.* in //')
  url=$(grep -oE 'http://localhost:[0-9]+' "$log" | tail -1)
  echo "project : $id"
  echo "dir     : $proj"
  echo "url     : $url"

  if ! grep -q '"operation": "llm' "$proj/repairs.json" 2>/dev/null; then
    echo "FAIL: no LLM repair provenance found"
    cat "$proj/repairs.json" 2>/dev/null || true
    fail=1
  fi

  df=$(ls "$proj"/Dockerfile.* 2>/dev/null | head -1)
  echo "manifest: $df"
  if ! grep -n "$expect" "$df" >/dev/null 2>&1; then
    echo "FAIL: expected '$expect' in $df"
    fail=1
  fi

  ok=0
  for _ in $(seq 1 25); do
    if curl -fsS --max-time 3 "$url/" >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != "1" ]; then
    echo "FAIL: endpoint did not become healthy: $url"
    "$BIN" --no-color status "$id" | head -12
    fail=1
  else
    echo "endpoint: HTTP OK"
  fi

  echo "--- heal trail ---"
  grep -E 'build failed|runtime|🔧|healthy|attempt|Stack healthy' "$log" | tail -14 || true
  echo "--- provenance ---"
  python3 - "$proj/repairs.json" <<'PY' 2>/dev/null || true
import json,sys
d=json.load(open(sys.argv[1]))
for r in d.get("repairs",[]):
    print(f"  {r.get('file')} | {r.get('operation')} | {r.get('failure_category')} | {r.get('summary','')[:80]}")
PY
}

run_case llm-runtime-heal 'application'
run_case llm-build-heal 'git'

run_failure_case() {
  name=unfixable-source
  log="/tmp/heal-$name.log"
  echo
  echo "=============================================================="
  echo "CASE: $name (expected to fail honestly)"
  echo "=============================================================="
  if timeout 600 "$BIN" --no-color init "$ROOT/test/fixtures/$name" >"$log" 2>&1; then
    echo "FAIL: expected init to exit non-zero"
    fail=1
    return
  fi
  if ! grep -q "stack could not be brought up" "$log"; then
    echo "FAIL: missing actionable failure summary"
    fail=1
  fi
  if ! grep -q "NEXT STEPS" "$log"; then
    echo "FAIL: missing next steps"
    fail=1
  fi
  proj=$(grep -oE 'manifest\(s\) in .*' "$log" | head -1 | sed 's/.* in //')
  if [ -n "${proj:-}" ]; then
    if ! (cd "$proj" && docker compose -f docker-compose.yml config --quiet); then
      echo "FAIL: compose left invalid"
      fail=1
    fi
    if grep -qE 'ignoreBuildErrors|no-type-check|npm install -g|NEXT_TELEMETRY' "$proj"/Dockerfile.* 2>/dev/null; then
      echo "FAIL: forbidden/failed patch left in Dockerfile"
      fail=1
    fi
  fi
  echo "expected failure: reported cleanly, manifests rolled back"
}

run_failure_case

echo
if [ "$fail" -eq 0 ]; then
  echo "LIVE HEAL TEST: PASS (LLM repaired both failures; unfixable case failed cleanly)"
else
  echo "LIVE HEAL TEST: FAIL"
fi
exit "$fail"