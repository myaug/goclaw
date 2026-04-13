#!/usr/bin/env bash
# fly-deploy.sh — Build and deploy aixio to Fly.io.
# Run after `fly-setup.sh` has completed.
#
# Usage:
#   bash scripts/fly-deploy.sh           # auto-detect version from git tags
#   VERSION=1.2.3 bash scripts/fly-deploy.sh
set -euo pipefail

APP="aixio"

# ── Helpers ──────────────────────────────────────────────────────────────────
bold()  { echo -e "\033[1m$*\033[0m"; }
green() { echo -e "\033[0;32m$*\033[0m"; }
red()   { echo -e "\033[0;31m$*\033[0m"; }

step() { echo; bold "==> $*"; }

# ── Version ───────────────────────────────────────────────────────────────────
if [[ -z "${VERSION:-}" ]]; then
  VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
fi

# ── Pre-flight ────────────────────────────────────────────────────────────────
if ! command -v flyctl &>/dev/null; then
  red "[!] flyctl not found. Install: https://fly.io/docs/hands-on/install-flyctl/"
  exit 1
fi

flyctl auth whoami &>/dev/null || {
  red "[!] Not logged in. Run: flyctl auth login"
  exit 1
}

# ── Validate required secrets are set ────────────────────────────────────────
step "Checking required secrets"

MISSING=()
SECRETS=$(flyctl secrets list --app "$APP" 2>/dev/null || true)

for KEY in GOCLAW_POSTGRES_DSN GOCLAW_GATEWAY_TOKEN GOCLAW_ENCRYPTION_KEY; do
  if ! echo "$SECRETS" | grep -q "$KEY"; then
    MISSING+=("$KEY")
  fi
done

if [[ ${#MISSING[@]} -gt 0 ]]; then
  red "[!] Missing required secrets: ${MISSING[*]}"
  echo "    Run: bash scripts/fly-setup.sh"
  exit 1
fi

echo "    All required secrets present."

# ── Deploy ────────────────────────────────────────────────────────────────────
step "Deploying $APP (version: $VERSION)"
echo "    Build args: ENABLE_REDIS=true  ENABLE_EMBEDUI=true  ENABLE_FULL_SKILLS=true"
echo "    Runtime: Redis DSN optional (falls back to in-memory cache)"
echo "    Remote build: yes"
echo

flyctl deploy \
  --app "$APP" \
  --build-arg VERSION="$VERSION" \
  --remote-only \
  --wait-timeout 300

# ── Post-deploy status ────────────────────────────────────────────────────────
echo
green "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
green "  Deploy complete!"
green "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  URL: https://$APP.fly.dev"
echo

flyctl status --app "$APP"
