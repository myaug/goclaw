#!/usr/bin/env bash
# fly-add-telegram-mcp.sh — register/refresh telegram-mcp server on Fly deployment.
#
# Usage:
#   bash scripts/fly-add-telegram-mcp.sh
#
# Optional env vars:
#   GOCLAW_URL               Gateway base URL (default: https://aixio.fly.dev)
#   GOCLAW_GATEWAY_TOKEN     Gateway bearer token
#   GOCLAW_AGENT_ID          Agent UUID to grant MCP access (optional)
#   TELEGRAM_API_ID          Telegram API ID (required)
#   TELEGRAM_API_HASH        Telegram API hash (required)
#   TELEGRAM_SESSION_STRING  Telegram session string (required if TELEGRAM_SESSION_NAME is empty)
#   TELEGRAM_SESSION_NAME    Telegram session name (required if TELEGRAM_SESSION_STRING is empty)
set -euo pipefail

APP="aixio"
GOCLAW_URL="${GOCLAW_URL:-https://${APP}.fly.dev}"

bold()  { echo -e "\033[1m$*\033[0m"; }
green() { echo -e "\033[0;32m$*\033[0m"; }
yellow(){ echo -e "\033[0;33m$*\033[0m"; }
red()   { echo -e "\033[0;31m$*\033[0m"; }
step()  { echo; bold "==> $*"; }

if ! command -v curl >/dev/null 2>&1; then
  red "[!] curl is required"
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  red "[!] python3 is required"
  exit 1
fi

if [[ -z "${GOCLAW_GATEWAY_TOKEN:-}" ]]; then
  read -rsp "Gateway token (GOCLAW_GATEWAY_TOKEN): " GOCLAW_GATEWAY_TOKEN
  echo
fi

if [[ -z "${GOCLAW_GATEWAY_TOKEN:-}" ]]; then
  red "[!] GOCLAW_GATEWAY_TOKEN is required"
  exit 1
fi

if [[ -z "${TELEGRAM_API_ID:-}" ]]; then
  read -rp "TELEGRAM_API_ID: " TELEGRAM_API_ID
fi
if [[ -z "${TELEGRAM_API_HASH:-}" ]]; then
  read -rsp "TELEGRAM_API_HASH: " TELEGRAM_API_HASH
  echo
fi
if [[ -z "${TELEGRAM_SESSION_STRING:-}" && -z "${TELEGRAM_SESSION_NAME:-}" ]]; then
  read -rsp "TELEGRAM_SESSION_STRING (or leave empty to use TELEGRAM_SESSION_NAME): " TELEGRAM_SESSION_STRING
  echo
  if [[ -z "${TELEGRAM_SESSION_STRING:-}" ]]; then
    read -rp "TELEGRAM_SESSION_NAME: " TELEGRAM_SESSION_NAME
  fi
fi

if [[ -z "${TELEGRAM_API_ID:-}" || -z "${TELEGRAM_API_HASH:-}" ]]; then
  red "[!] TELEGRAM_API_ID and TELEGRAM_API_HASH are required"
  exit 1
fi
if [[ -z "${TELEGRAM_SESSION_STRING:-}" && -z "${TELEGRAM_SESSION_NAME:-}" ]]; then
  red "[!] Provide TELEGRAM_SESSION_STRING or TELEGRAM_SESSION_NAME"
  exit 1
fi

if [[ -z "${GOCLAW_AGENT_ID:-}" ]]; then
  read -rp "Agent UUID to grant access (optional, Enter to skip): " GOCLAW_AGENT_ID || true
fi

step "Checking gateway health"
if ! curl -fsS "${GOCLAW_URL}/health" >/dev/null; then
  red "[!] Gateway is not reachable at ${GOCLAW_URL}"
  exit 1
fi

step "Building MCP server payload"
PAYLOAD=$(python3 - <<'PY'
import json, os

env = {
    "TELEGRAM_API_ID": os.getenv("TELEGRAM_API_ID", ""),
    "TELEGRAM_API_HASH": os.getenv("TELEGRAM_API_HASH", ""),
}
if os.getenv("TELEGRAM_SESSION_STRING"):
    env["TELEGRAM_SESSION_STRING"] = os.getenv("TELEGRAM_SESSION_STRING")
if os.getenv("TELEGRAM_SESSION_NAME"):
    env["TELEGRAM_SESSION_NAME"] = os.getenv("TELEGRAM_SESSION_NAME")

payload = {
    "name": "telegram-mcp",
    "display_name": "Telegram MCP",
    "transport": "stdio",
    "command": "telegram-mcp",
    "args": [],
    "enabled": True,
    "tool_prefix": "tg",
    "timeout_sec": 120,
    "env": env,
}
print(json.dumps(payload))
PY
)

AUTH_HEADER="Authorization: Bearer ${GOCLAW_GATEWAY_TOKEN}"

step "Looking up existing MCP server"
LIST_JSON=$(curl -fsS "${GOCLAW_URL}/v1/mcp/servers" -H "$AUTH_HEADER")
SERVER_ID=$(printf '%s' "$LIST_JSON" | python3 -c 'import json,sys
data=json.load(sys.stdin)
for s in data.get("servers", []):
  if s.get("name") == "telegram-mcp":
    print(s.get("id", ""))
    break')

if [[ -n "$SERVER_ID" ]]; then
  step "Updating existing MCP server (id: ${SERVER_ID})"
  curl -fsS -X PUT "${GOCLAW_URL}/v1/mcp/servers/${SERVER_ID}" \
    -H "$AUTH_HEADER" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" >/dev/null
  green "    ✓ Updated MCP server 'telegram-mcp'"
else
  step "Creating MCP server"
  CREATE_JSON=$(curl -fsS -X POST "${GOCLAW_URL}/v1/mcp/servers" \
    -H "$AUTH_HEADER" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD")
  SERVER_ID=$(printf '%s' "$CREATE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id", ""))')
  if [[ -z "$SERVER_ID" ]]; then
    red "[!] Created server but failed to read server ID"
    exit 1
  fi
  green "    ✓ Created MCP server 'telegram-mcp'"
fi

if [[ -n "${GOCLAW_AGENT_ID:-}" ]]; then
  step "Granting access to agent ${GOCLAW_AGENT_ID}"
  GRANT_PAYLOAD=$(printf '{"agent_id":"%s","enabled":true}' "$GOCLAW_AGENT_ID")
  curl -fsS -X POST "${GOCLAW_URL}/v1/mcp/servers/${SERVER_ID}/grants/agent" \
    -H "$AUTH_HEADER" \
    -H "Content-Type: application/json" \
    -d "$GRANT_PAYLOAD" >/dev/null
  green "    ✓ Grant applied"
else
  yellow "    ! No agent UUID provided; remember to grant this MCP server from UI/API"
fi

echo
green "Done. telegram-mcp is registered on ${GOCLAW_URL}."
