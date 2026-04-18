#!/usr/bin/env bash
# fly-setup.sh — One-time setup of the aixio app on Fly.io.
# Run this once before the first deploy.
#
# Usage:
#   bash scripts/fly-setup.sh
#
# Optional env vars:
#   FLY_REGION  — Fly.io region (default: sin = Singapore)
set -euo pipefail

APP="aixio"
REGION="${FLY_REGION:-sin}"

# ── Helpers ─────────────────────────────────────────────────────────────────
red()    { echo -e "\033[0;31m$*\033[0m"; }
green()  { echo -e "\033[0;32m$*\033[0m"; }
yellow() { echo -e "\033[0;33m$*\033[0m"; }
bold()   { echo -e "\033[1m$*\033[0m"; }

step() { echo; bold "==> $*"; }
ok()   { green "    ✓ $*"; }
warn() { yellow "    ! $*"; }

# ── Pre-flight checks ────────────────────────────────────────────────────────
if ! command -v flyctl &>/dev/null; then
  red "[!] flyctl not found."
  echo "    Install: https://fly.io/docs/hands-on/install-flyctl/"
  exit 1
fi

if ! command -v openssl &>/dev/null; then
  red "[!] openssl not found (needed to generate secure tokens)."
  exit 1
fi

step "Checking Fly.io auth"
flyctl auth whoami || {
  red "[!] Not logged in. Run: flyctl auth login"
  exit 1
}

# ── Create app ───────────────────────────────────────────────────────────────
step "Creating Fly.io app '$APP'"
if flyctl status --app "$APP" &>/dev/null; then
  ok "App already exists, skipping creation."
else
  flyctl apps create "$APP"
  ok "App created."
fi

# ── Create persistent volume ─────────────────────────────────────────────────
step "Creating persistent volume in region '$REGION'"

VOL="goclaw_data"
if flyctl volumes list --app "$APP" 2>/dev/null | grep -q "$VOL"; then
  ok "Volume '$VOL' already exists, skipping."
else
  flyctl volumes create "$VOL" \
    --app "$APP" \
    --region "$REGION" \
    --size 2 \
    --yes
  ok "Volume '$VOL' created (2 GB)."
fi

# ── Collect secrets ──────────────────────────────────────────────────────────
step "Collecting required secrets"
echo
echo "  Supabase PostgreSQL connection string."
echo "  Session pooler (port 6543) is recommended for production; direct (5432) also works."
echo "  Example (session pooler):"
echo "    postgresql://postgres.[ref]:[password]@aws-0-[region].pooler.supabase.com:6543/postgres?sslmode=require"
echo "  Example (direct connection):"
echo "    postgresql://postgres:[password]@db.[ref].supabase.co:5432/postgres?sslmode=require"
echo
read -rsp "  Paste your Supabase DSN: " POSTGRES_DSN
echo

if [[ -z "$POSTGRES_DSN" ]]; then
  red "[!] Supabase DSN cannot be empty."
  exit 1
fi

echo
echo "  Optional: Redis connection string."
echo "  Use rediss:// (TLS) for Redis Cloud public endpoints."
echo "  Leave blank to use the gateway's in-memory cache."
echo "  Example: rediss://default:[password]@[host]:[port]"
echo
read -rsp "  Paste your Redis DSN (leave blank to skip): " REDIS_DSN
echo

# ── Generate secure tokens ───────────────────────────────────────────────────
GATEWAY_TOKEN=$(openssl rand -hex 32)
ENCRYPTION_KEY=$(openssl rand -hex 32)

echo
bold "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
bold "  Generated credentials — SAVE THESE NOW"
bold "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  GOCLAW_GATEWAY_TOKEN  = $GATEWAY_TOKEN"
echo "  GOCLAW_ENCRYPTION_KEY = $ENCRYPTION_KEY"
bold "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Store these securely (password manager, etc.)."
echo "  GATEWAY_TOKEN is required by clients; ENCRYPTION_KEY decrypts stored API keys."
echo

# ── Push secrets to Fly.io ──────────────────────────────────────────────────
step "Setting Fly.io secrets"

SECRET_ARGS=(
  "GOCLAW_POSTGRES_DSN=$POSTGRES_DSN"
  "GOCLAW_GATEWAY_TOKEN=$GATEWAY_TOKEN"
  "GOCLAW_ENCRYPTION_KEY=$ENCRYPTION_KEY"
)
if [[ -n "$REDIS_DSN" ]]; then
  SECRET_ARGS+=("GOCLAW_REDIS_DSN=$REDIS_DSN")
fi

flyctl secrets set "${SECRET_ARGS[@]}" --app "$APP"

ok "Secrets stored."
if [[ -z "$REDIS_DSN" ]]; then
  warn "GOCLAW_REDIS_DSN not set; the gateway will use in-memory cache."
fi

# ── Optional LLM API keys ────────────────────────────────────────────────────
step "Optional: LLM provider API keys"
echo "  You can set these now or later with: flyctl secrets set KEY=VALUE --app $APP"
echo
echo "  Common keys:"
echo "    GOCLAW_ANTHROPIC_API_KEY   — Claude (Anthropic)"
echo "    GOCLAW_OPENAI_API_KEY      — GPT / o-series"
echo "    GOCLAW_OPENROUTER_API_KEY  — OpenRouter"
echo "    GOCLAW_GEMINI_API_KEY      — Gemini (Google)"
echo ""
echo "  Optional tokens for idea-reality MCP (if you plan to enable it):"
echo "    GITHUB_TOKEN               — higher GitHub API limits"
echo "    PRODUCTHUNT_TOKEN          — enable Product Hunt in deep mode"
echo ""
echo "  Optional secrets for telegram-mcp:"
echo "    TELEGRAM_API_ID            — required by telegram-mcp"
echo "    TELEGRAM_API_HASH          — required by telegram-mcp"
echo "    TELEGRAM_SESSION_STRING    — recommended session auth"
echo "    TELEGRAM_SESSION_NAME      — optional if using named session file"
echo

read -rp "  Set API keys now? [y/N] " SETKEYS
if [[ "${SETKEYS,,}" == "y" ]]; then
  declare -A LLMKEYS=(
    [GOCLAW_ANTHROPIC_API_KEY]="Anthropic (Claude)"
    [GOCLAW_OPENAI_API_KEY]="OpenAI (GPT/o-series)"
    [GOCLAW_OPENROUTER_API_KEY]="OpenRouter"
    [GOCLAW_GEMINI_API_KEY]="Google Gemini"
    [GITHUB_TOKEN]="Idea Reality MCP (GitHub token, optional)"
    [PRODUCTHUNT_TOKEN]="Idea Reality MCP (Product Hunt token, optional)"
    [TELEGRAM_API_ID]="Telegram MCP (API ID, optional)"
    [TELEGRAM_API_HASH]="Telegram MCP (API hash, optional)"
    [TELEGRAM_SESSION_STRING]="Telegram MCP (session string, optional)"
    [TELEGRAM_SESSION_NAME]="Telegram MCP (session name, optional)"
  )
  SECRET_ARGS=()
  for KEY in "${!LLMKEYS[@]}"; do
    read -rsp "  ${LLMKEYS[$KEY]} — $KEY (leave blank to skip): " VAL
    echo
    [[ -n "$VAL" ]] && SECRET_ARGS+=("$KEY=$VAL")
  done
  if [[ ${#SECRET_ARGS[@]} -gt 0 ]]; then
    flyctl secrets set "${SECRET_ARGS[@]}" --app "$APP"
    ok "LLM keys stored."
  fi
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo
green "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
green "  Setup complete!"
green "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Next step: deploy the app"
echo "    bash scripts/fly-deploy.sh"
echo
echo "  After deploy, your gateway will be at:"
echo "    https://$APP.fly.dev"
echo
echo "  To add more secrets later:"
echo "    flyctl secrets set KEY=VALUE --app $APP"
echo
