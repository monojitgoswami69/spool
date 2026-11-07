#!/usr/bin/env sh
# Spool installer — builds from source with the local Go toolchain.
set -eu

MODULE="github.com/monojitgoswami69/spool"
REPO="https://github.com/monojitgoswami69/spool.git"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-v0.4.0}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "Go 1.27+ is required (https://go.dev/dl/)"
command -v git >/dev/null 2>&1 || die "git is required"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Cloning Spool..."
git clone --depth=1 "$REPO" "$tmp/spool" >/dev/null 2>&1 || die "clone failed"

say "Building..."
(
  cd "$tmp/spool"
  go build -trimpath \
    -ldflags "-s -w -X ${MODULE}/cmd.Version=${VERSION} -X ${MODULE}/cmd.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o spool .
)

mkdir -p "$BIN_DIR"
install -m 755 "$tmp/spool/spool" "$BIN_DIR/spool"
say "Installed spool to $BIN_DIR/spool"

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *) say "Add $BIN_DIR to your PATH to run 'spool' from anywhere." ;;
esac