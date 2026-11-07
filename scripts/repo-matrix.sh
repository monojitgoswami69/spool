#!/usr/bin/env bash
# Broad real-repo generation matrix: clone + scan + generate + compose validate.
# Does not start containers. Override REPOS with a newline-separated list to test others.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
BIN=${BIN:-$ROOT/spool}
export SPOOL_HOME=${SPOOL_HOME:-/tmp/spool-matrix-home}
OUT=${OUT:-/tmp/spool-matrix}
rm -rf "$SPOOL_HOME"
mkdir -p "$SPOOL_HOME" "$OUT"

DEFAULT_REPOS="
https://github.com/monojitgoswami69/collabify
https://github.com/monojitgoswami69/Tallo
https://github.com/monojitgoswami69/blogs
https://github.com/monojitgoswami69/gitopsy
https://github.com/monojitgoswami69/Innofusion3
https://github.com/monojitgoswami69/nohara-family-webmaniacs
https://github.com/monojitgoswami69/certify
https://github.com/monojitgoswami69/Sevatra
https://github.com/monojitgoswami69/FundMySkill
https://github.com/monojitgoswami69/codalyzer
https://github.com/monojitgoswami69/roomi
https://github.com/monojitgoswami69/campus-dost
https://github.com/monojitgoswami69/nexus
https://github.com/monojitgoswami69/portfolio-manager
https://github.com/monojitgoswami69/BI_Agent
https://github.com/monojitgoswami69/hacktropica
https://github.com/monojitgoswami69/frosthacks
https://github.com/monojitgoswami69/github-readme-stats
https://github.com/monojitgoswami69/Shiksha_Saathi
https://github.com/monojitgoswami69/Python-Games
https://github.com/monojitgoswami69/chimera
https://github.com/monojitgoswami69/yoink
https://github.com/monojitgoswami69/github-readme-streak-stats
"

REPOS=${REPOS:-$DEFAULT_REPOS}
pass=0
fail=0
noservice=0

for url in $REPOS; do
  name=$(basename "$url")
  log="$OUT/$name.log"
  start=$(date +%s)
  timeout 300 "$BIN" --no-color init "$url" --no-run --no-agent >"$log" 2>&1
  code=$?
  dur=$(( $(date +%s) - start ))
  svc=$(grep -oE '[0-9]+ service\(s\):.*' "$log" | head -1)
  inf=$(grep -oE 'inferred:.*' "$log" | head -1)
  cc="-"
  if [ $code -eq 0 ]; then
    pass=$((pass+1))
    proj=$(grep -oE 'manifest\(s\) in .*' "$log" | head -1 | sed 's/.* in //')
    if [ -n "${proj:-}" ] && (cd "$proj" && docker compose -f docker-compose.yml config >/dev/null 2>/tmp/compose-err); then
      cc="OK"
    else
      cc="FAIL"
      fail=$((fail+1))
    fi
  elif grep -q "no supported services detected" "$log"; then
    noservice=$((noservice+1))
  else
    fail=$((fail+1))
  fi
  printf '%-32s exit=%-3s %-3ss compose=%-4s | %s | %s\n' "$name" "$code" "$dur" "$cc" "${svc:-}" "${inf:-}"
  if [ $code -ne 0 ] && ! grep -q "no supported services detected" "$log"; then
    grep -E '✗|error|Error|panic' "$log" | tail -2 | sed 's/^/    /'
  fi
done

echo
echo "matrix: $pass generated, $noservice correctly had no services, $fail failures"
[ "$fail" -eq 0 ]