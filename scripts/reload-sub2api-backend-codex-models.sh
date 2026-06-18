#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="$PROJECT_DIR/backend"
SESSION_NAME="sub2api"
BACKEND_PORT="${BACKEND_PORT:-8080}"
CANARY_PORT="${CANARY_PORT:-18080}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
REDIS_PORT="${REDIS_PORT:-6379}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-$(openssl rand -hex 18 2>/dev/null || head -c 36 /dev/urandom | xxd -p | tr -d '\n' | head -c 36)}"
JWT_SECRET="${JWT_SECRET:-$(openssl rand -hex 32 2>/dev/null || head -c 64 /dev/urandom | xxd -p | tr -d '\n' | head -c 64)}"
RUN_DIR="${RUN_DIR:-/tmp/sub2api-codex-models-reload}"
MODE="${1:---canary-only}"
DELAY_SECONDS=0

if [[ "$MODE" == "--execute" && "${2:-}" == "--delay" ]]; then
  DELAY_SECONDS="${3:-0}"
fi

mkdir -p "$RUN_DIR"
BIN="$RUN_DIR/sub2api-server"
CANARY_LOG="$RUN_DIR/canary.log"
RELOAD_LOG="$RUN_DIR/reload.log"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$RELOAD_LOG"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    log "missing required command: $1"
    exit 1
  fi
}

auth_token() {
  /usr/bin/python3 - <<'PY'
import json
import pathlib

path = pathlib.Path.home() / ".codex" / "auth.json"
print(json.loads(path.read_text()).get("OPENAI_API_KEY", ""))
PY
}

wait_for_health() {
  local port="$1"
  local waited=0
  while (( waited < 60 )); do
    if curl -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

find_canary_port() {
  local port="$CANARY_PORT"
  while (( port < CANARY_PORT + 100 )); do
    if ! lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "$port"
      return
    fi
    port=$((port + 1))
  done
  return 1
}

start_canary() {
  local port="$1"
  log "starting canary on port ${port}"
  (
    cd "$BACKEND_DIR"
    AUTO_SETUP=true \
      DATABASE_HOST=127.0.0.1 \
      DATABASE_PORT="$POSTGRES_PORT" \
      DATABASE_USER=sub2api \
      DATABASE_PASSWORD=sub2api \
      DATABASE_DBNAME=sub2api \
      DATABASE_SSLMODE=disable \
      ADMIN_EMAIL="$ADMIN_EMAIL" \
      ADMIN_PASSWORD="$ADMIN_PASSWORD" \
      JWT_SECRET="$JWT_SECRET" \
      SERVER_PORT="$port" \
      SERVER_HOST=127.0.0.1 \
      REDIS_HOST=127.0.0.1 \
      REDIS_PORT="$REDIS_PORT" \
      GATEWAY_DEFAULT_PROXY_URL=http://127.0.0.1:7897 \
      TZ=Asia/Shanghai \
      "$BIN"
  ) >"$CANARY_LOG" 2>&1 &
  CANARY_PID=$!
}

stop_canary() {
  if [[ -n "${CANARY_PID:-}" ]] && kill -0 "$CANARY_PID" >/dev/null 2>&1; then
    log "stopping canary pid ${CANARY_PID}"
    kill "$CANARY_PID" >/dev/null 2>&1 || true
    wait "$CANARY_PID" >/dev/null 2>&1 || true
  fi
}

validate_codex_models() {
  local port="$1"
  local token="$2"
  local response_file="$RUN_DIR/codex-models.json"
  curl -fsS \
    -H "Authorization: Bearer ${token}" \
    "http://127.0.0.1:${port}/v1/models?client_version=0.140.0" \
    >"$response_file"
  /usr/bin/python3 - "$response_file" <<'PY'
import json
import sys

data = json.loads(open(sys.argv[1], encoding="utf-8").read())
slugs = [model.get("slug") for model in data.get("models", [])]
missing = [slug for slug in ("deepseek-v4-pro", "deepseek-v4-flash") if slug not in slugs]
if missing:
    raise SystemExit(f"missing codex model slug(s): {', '.join(missing)}")
PY
}

reload_active_backend() {
  local command_text
  command_text="AUTO_SETUP=true DATABASE_HOST=127.0.0.1 DATABASE_PORT=${POSTGRES_PORT} DATABASE_USER=sub2api DATABASE_PASSWORD=sub2api DATABASE_DBNAME=sub2api DATABASE_SSLMODE=disable ADMIN_EMAIL=${ADMIN_EMAIL} ADMIN_PASSWORD=${ADMIN_PASSWORD} JWT_SECRET=${JWT_SECRET} SERVER_PORT=${BACKEND_PORT} SERVER_HOST=0.0.0.0 REDIS_HOST=127.0.0.1 REDIS_PORT=${REDIS_PORT} GATEWAY_DEFAULT_PROXY_URL=http://127.0.0.1:7897 TZ=Asia/Shanghai ${BIN}"

  log "respawning tmux backend pane on port ${BACKEND_PORT}"
  tmux respawn-pane -k -t "${SESSION_NAME}:backend" -c "$BACKEND_DIR" "$command_text"
  if ! wait_for_health "$BACKEND_PORT"; then
    log "active backend failed health check; inspect tmux pane ${SESSION_NAME}:backend"
    exit 1
  fi
  log "active backend health check passed"

  if command -v python3 >/dev/null 2>&1 && [[ -f "$PROJECT_DIR/port-registry.py" ]]; then
    python3 "$PROJECT_DIR/port-registry.py" register "$SESSION_NAME" \
      "{\"backend\":${BACKEND_PORT},\"frontend\":12333,\"postgres\":${POSTGRES_PORT},\"redis\":${REDIS_PORT}}" \
      "running" >>"$RELOAD_LOG" 2>&1 || true
  fi
}

main() {
  require_command go
  require_command curl
  require_command lsof
  require_command tmux
  require_command /usr/bin/python3

  local token
  token="$(auth_token)"
  if [[ -z "$token" ]]; then
    log "OPENAI_API_KEY missing in ~/.codex/auth.json"
    exit 1
  fi

  log "building backend binary at ${BIN}"
  (cd "$BACKEND_DIR" && go build -o "$BIN" ./cmd/server)

  local actual_canary_port
  actual_canary_port="$(find_canary_port)"
  trap stop_canary EXIT
  start_canary "$actual_canary_port"

  if ! wait_for_health "$actual_canary_port"; then
    log "canary failed health check; tail ${CANARY_LOG}"
    exit 1
  fi
  log "canary health check passed"

  validate_codex_models "$actual_canary_port" "$token"
  log "canary Codex model catalog includes deepseek-v4-pro and deepseek-v4-flash"

  if [[ "$MODE" == "--canary-only" ]]; then
    log "canary-only mode complete; active backend unchanged"
    return
  fi

  if [[ "$MODE" != "--execute" ]]; then
    log "unknown mode: ${MODE}"
    exit 1
  fi

  stop_canary
  trap - EXIT

  if (( DELAY_SECONDS > 0 )); then
    log "waiting ${DELAY_SECONDS}s before active backend reload"
    sleep "$DELAY_SECONDS"
  fi

  reload_active_backend
  validate_codex_models "$BACKEND_PORT" "$token"
  log "active Codex model catalog includes deepseek-v4-pro and deepseek-v4-flash"
}

main
