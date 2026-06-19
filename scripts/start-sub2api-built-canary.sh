#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="$PROJECT_DIR/backend"
RUN_DIR="${RUN_DIR:-$PROJECT_DIR/.run/sub2api-built-canary}"

SESSION_NAME="${SESSION_NAME:-sub2api-built-canary}"
BACKEND_PORT="${BACKEND_PORT:-18081}"
FRONTEND_PORT="${FRONTEND_PORT:-12334}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_DB="${REDIS_DB:-15}"
DATABASE_USER="${DATABASE_USER:-sub2api}"
DATABASE_PASSWORD="${DATABASE_PASSWORD:-sub2api}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-$(openssl rand -hex 18 2>/dev/null || head -c 36 /dev/urandom | xxd -p | tr -d '\n' | head -c 36)}"
JWT_SECRET="${JWT_SECRET:-$(openssl rand -hex 32 2>/dev/null || head -c 64 /dev/urandom | xxd -p | tr -d '\n' | head -c 64)}"
GATEWAY_DEFAULT_PROXY_URL="${GATEWAY_DEFAULT_PROXY_URL:-http://127.0.0.1:7897}"
BIN="${BIN:-$BACKEND_DIR/bin/server-embed}"

REPLACE=0
ATTACH=0
BUILD=0

usage() {
  cat <<EOF
Usage: $(basename "$0") [--replace] [--attach] [--build]

Starts a compiled Sub2API canary in tmux without touching the active sub2api
session on ports 8080/12333.

Defaults:
  session:       $SESSION_NAME
  backend start: $BACKEND_PORT
  frontend start:$FRONTEND_PORT
  binary:        $BIN

Options:
  --replace   Kill only this canary session if it already exists.
  --attach    Attach to the tmux session after startup.
  --build     Rebuild frontend assets and backend embed binary before startup.
  --help      Show this help.

Environment overrides:
  SESSION_NAME BACKEND_PORT FRONTEND_PORT POSTGRES_PORT REDIS_PORT REDIS_DB
  DATABASE_USER DATABASE_PASSWORD ADMIN_EMAIL ADMIN_PASSWORD JWT_SECRET
  GATEWAY_DEFAULT_PROXY_URL BIN RUN_DIR
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --replace)
      REPLACE=1
      shift
      ;;
    --attach)
      ATTACH=1
      shift
      ;;
    --build)
      BUILD=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

port_is_free() {
  local port="$1"
  ! lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
}

find_free_port() {
  local start="$1"
  local port="$start"
  while (( port < start + 100 )); do
    if port_is_free "$port"; then
      echo "$port"
      return 0
    fi
    port=$((port + 1))
  done
  return 1
}

wait_for_http() {
  local url="$1"
  local seconds="${2:-60}"
  local waited=0
  while (( waited < seconds )); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

build_artifacts() {
  log "Building frontend production assets"
  (cd "$PROJECT_DIR" && pnpm --dir frontend run build)

  log "Building backend embed binary: $BIN"
  local version
  version="$(tr -d '\r\n' < "$BACKEND_DIR/cmd/server/VERSION")"
  (cd "$BACKEND_DIR" && CGO_ENABLED=0 go build -tags embed -ldflags="-s -w -X main.Version=${version}" -trimpath -o "$BIN" ./cmd/server)
}

write_frontend_proxy() {
  mkdir -p "$RUN_DIR"
  cat >"$RUN_DIR/frontend-proxy.js" <<'NODE'
const http = require("http");

const backend = new URL(process.env.BACKEND_URL);
const listenHost = process.env.FRONTEND_HOST || "127.0.0.1";
const listenPort = Number(process.env.FRONTEND_PORT);

const server = http.createServer((req, res) => {
  const headers = { ...req.headers, host: backend.host };
  const options = {
    protocol: backend.protocol,
    hostname: backend.hostname,
    port: backend.port,
    method: req.method,
    path: req.url,
    headers,
  };

  const upstream = http.request(options, (upstreamRes) => {
    res.writeHead(upstreamRes.statusCode || 502, upstreamRes.headers);
    upstreamRes.pipe(res);
  });

  upstream.on("error", (err) => {
    res.writeHead(502, { "content-type": "text/plain; charset=utf-8" });
    res.end(`frontend proxy error: ${err.message}\n`);
  });

  req.pipe(upstream);
});

server.listen(listenPort, listenHost, () => {
  console.log(`frontend proxy listening on http://${listenHost}:${listenPort}`);
  console.log(`proxying to ${backend.href}`);
});
NODE
}

register_ports() {
  if command -v python3 >/dev/null 2>&1 && [[ -f "$PROJECT_DIR/port-registry.py" ]]; then
    python3 "$PROJECT_DIR/port-registry.py" register "$SESSION_NAME" \
      "{\"backend\":${ACTUAL_BACKEND_PORT},\"frontend\":${ACTUAL_FRONTEND_PORT},\"postgres\":${POSTGRES_PORT},\"redis\":${REDIS_PORT}}" \
      "testing" >/dev/null
  fi
}

unregister_ports() {
  if command -v python3 >/dev/null 2>&1 && [[ -f "$PROJECT_DIR/port-registry.py" ]]; then
    python3 "$PROJECT_DIR/port-registry.py" unregister "$SESSION_NAME" >/dev/null 2>&1 || true
  fi
}

main() {
  require_command tmux
  require_command lsof
  require_command curl
  require_command go
  require_command pnpm
  require_command node

  if [[ "$BUILD" -eq 1 ]]; then
    build_artifacts
  fi

  if [[ ! -x "$BIN" ]]; then
    echo "Missing executable embed binary: $BIN" >&2
    echo "Run: $0 --build" >&2
    exit 1
  fi

  if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
    if [[ "$REPLACE" -ne 1 ]]; then
      echo "tmux session '$SESSION_NAME' already exists. Use --replace to restart only this canary session." >&2
      exit 1
    fi
    log "Stopping existing canary tmux session: $SESSION_NAME"
    tmux kill-session -t "$SESSION_NAME"
    unregister_ports
  fi

  ACTUAL_BACKEND_PORT="$(find_free_port "$BACKEND_PORT")"
  ACTUAL_FRONTEND_PORT="$(find_free_port "$FRONTEND_PORT")"
  if [[ -z "$ACTUAL_BACKEND_PORT" || -z "$ACTUAL_FRONTEND_PORT" ]]; then
    echo "Unable to find free canary ports" >&2
    exit 1
  fi
  if [[ "$ACTUAL_BACKEND_PORT" == "$ACTUAL_FRONTEND_PORT" ]]; then
    ACTUAL_FRONTEND_PORT="$(find_free_port "$((ACTUAL_BACKEND_PORT + 1))")"
  fi

  write_frontend_proxy

  BACKEND_LOG="$RUN_DIR/backend.log"
  FRONTEND_LOG="$RUN_DIR/frontend-proxy.log"

  log "Starting backend on 127.0.0.1:${ACTUAL_BACKEND_PORT}"
  tmux new-session -d -s "$SESSION_NAME" -n backend -c "$BACKEND_DIR" \
    "bash -lc 'AUTO_SETUP=true \
DATABASE_HOST=127.0.0.1 \
DATABASE_PORT=${POSTGRES_PORT} \
DATABASE_USER=${DATABASE_USER} \
DATABASE_PASSWORD=${DATABASE_PASSWORD} \
DATABASE_DBNAME=sub2api \
DATABASE_SSLMODE=disable \
ADMIN_EMAIL=${ADMIN_EMAIL} \
ADMIN_PASSWORD=${ADMIN_PASSWORD} \
JWT_SECRET=${JWT_SECRET} \
SERVER_PORT=${ACTUAL_BACKEND_PORT} \
SERVER_HOST=127.0.0.1 \
REDIS_HOST=127.0.0.1 \
REDIS_PORT=${REDIS_PORT} \
REDIS_DB=${REDIS_DB} \
GATEWAY_DEFAULT_PROXY_URL=${GATEWAY_DEFAULT_PROXY_URL} \
TZ=Asia/Shanghai \
${BIN} >${BACKEND_LOG} 2>&1; status=\$?; echo Backend exited with status \$status. Log: ${BACKEND_LOG}; tail -n 120 ${BACKEND_LOG}; exec bash -l'"

  if ! wait_for_http "http://127.0.0.1:${ACTUAL_BACKEND_PORT}/health" 90; then
    echo "Backend health check failed. Inspect: tmux capture-pane -t ${SESSION_NAME}:backend -p -S -120" >&2
    exit 1
  fi

  log "Starting frontend proxy on 127.0.0.1:${ACTUAL_FRONTEND_PORT}"
  tmux new-window -t "$SESSION_NAME" -n frontend -c "$PROJECT_DIR" \
    "bash -lc 'BACKEND_URL=http://127.0.0.1:${ACTUAL_BACKEND_PORT} FRONTEND_PORT=${ACTUAL_FRONTEND_PORT} node ${RUN_DIR}/frontend-proxy.js >${FRONTEND_LOG} 2>&1; status=\$?; echo Frontend proxy exited with status \$status. Log: ${FRONTEND_LOG}; tail -n 80 ${FRONTEND_LOG}; exec bash -l'"
  tmux new-window -t "$SESSION_NAME" -n logs -c "$PROJECT_DIR"

  if ! wait_for_http "http://127.0.0.1:${ACTUAL_FRONTEND_PORT}/health" 30; then
    echo "Frontend proxy health check failed. Inspect: tmux capture-pane -t ${SESSION_NAME}:frontend -p -S -80" >&2
    exit 1
  fi

  register_ports

  echo
  echo "Sub2API built canary is running."
  echo "  tmux session: $SESSION_NAME"
  echo "  backend API:  http://127.0.0.1:${ACTUAL_BACKEND_PORT}"
  echo "  frontend:     http://127.0.0.1:${ACTUAL_FRONTEND_PORT}"
  echo "  Codex base:   http://127.0.0.1:${ACTUAL_FRONTEND_PORT}/v1"
  echo
  echo "Stop canary:"
  echo "  tmux kill-session -t ${SESSION_NAME}"
  echo "  python3 ${PROJECT_DIR}/port-registry.py unregister ${SESSION_NAME}"
  echo

  if [[ "$ATTACH" -eq 1 ]]; then
    tmux attach -t "$SESSION_NAME"
  fi
}

main "$@"
