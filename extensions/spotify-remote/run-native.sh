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

framework_ctl() {
  ACTION="$1"
  if command -v "$ACTION" >/dev/null 2>&1; then
    "$ACTION" framework >> "$LOG_FILE" 2>&1 && return 0
  fi
  if [ -x /etc/init.d/framework ]; then
    /etc/init.d/framework "$ACTION" >> "$LOG_FILE" 2>&1 && return 0
  fi
  log "No framework $ACTION command available"
  return 1
}

stop_framework() {
  log "Stopping Kindle framework after launcher detach"
  touch "$FLAG_FILE"
  lipc-set-prop com.lab126.powerd preventScreenSaver 1 >/dev/null 2>&1 || true
  framework_ctl stop || true
  sleep 2
}

start_framework() {
  if [ -f "$FLAG_FILE" ]; then
    log "Restarting Kindle framework"
    rm -f "$FLAG_FILE"
    framework_ctl start || true
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
