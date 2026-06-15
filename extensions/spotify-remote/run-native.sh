#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$APP_DIR/bin/spotify-remote-arm"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/spotify-remote.pid"
LAUNCHER_PID_FILE="$APP_DIR/data/launcher.pid"
FLAG_FILE="/tmp/spotify-remote.framework-stopped"

mkdir -p "$APP_DIR/data" "$APP_DIR/logs"

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

stop_framework() {
  log "Stopping Kindle framework after launcher detach"
  touch "$FLAG_FILE"
  lipc-set-prop com.lab126.powerd preventScreenSaver 1 >/dev/null 2>&1 || true
  /etc/init.d/framework stop >> "$LOG_FILE" 2>&1 || true
  stop framework >> "$LOG_FILE" 2>&1 || true
  sleep 2
}

start_framework() {
  if [ -f "$FLAG_FILE" ]; then
    log "Restarting Kindle framework"
    rm -f "$FLAG_FILE"
    /etc/init.d/framework start >> "$LOG_FILE" 2>&1 || true
    start framework >> "$LOG_FILE" 2>&1 || true
    lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true
  fi
}

cleanup() {
  rm -f "$PID_FILE" "$LAUNCHER_PID_FILE"
  start_framework
}

trap cleanup EXIT INT TERM HUP

sleep 3

if [ ! -x "$BIN" ]; then
  log "ERROR: Binary missing or not executable: $BIN"
  if command -v eips >/dev/null 2>&1; then
    eips -c
    eips 2 0 "Spotify Remote"
    eips 4 0 "Binary missing or not executable."
    eips 6 0 "$BIN"
  fi
  exit 1
fi

stop_framework
cd "$APP_DIR" || exit 1
log "Running native binary"
"$BIN" >> "$LOG_FILE" 2>&1 &
APP_PID="$!"
echo "$APP_PID" > "$PID_FILE"
wait "$APP_PID"
STATUS="$?"
log "Native Spotify Remote exited with status $STATUS"
exit "$STATUS"
