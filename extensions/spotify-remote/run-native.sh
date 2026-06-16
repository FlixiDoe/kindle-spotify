#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$APP_DIR/bin/spotify-remote-arm"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/spotify-remote.pid"
LAUNCHER_PID_FILE="$APP_DIR/data/launcher.pid"
FLAG_FILE="/tmp/spotify-remote.framework-stopped"
MODE="$1"
FRAMEWORK_WAS_STOPPED=0
APP_STARTED=0
APP_PID=""
WAIT_STATUS=0
CLEANED_UP=0

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
  FRAMEWORK_WAS_STOPPED=1
  lipc-set-prop com.lab126.powerd preventScreenSaver 1 >/dev/null 2>&1 || true
  framework_ctl stop || true
  sleep 2
}

start_framework() {
  if [ "$FRAMEWORK_WAS_STOPPED" = "1" ] && [ -f "$FLAG_FILE" ]; then
    log "Restarting Kindle framework"
    rm -f "$FLAG_FILE"
    FRAMEWORK_WAS_STOPPED=0
    framework_ctl start || true
    lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true
  fi
}

cleanup() {
  if [ "$CLEANED_UP" = "1" ]; then
    return
  fi
  CLEANED_UP=1
  rm -f "$PID_FILE" "$LAUNCHER_PID_FILE"
  if [ "$APP_STARTED" = "1" ] || [ "$FRAMEWORK_WAS_STOPPED" = "1" ]; then
    start_framework
  fi
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
if [ "$MODE" = "ui" ]; then
  log "Running native binary in FBInk UI mode"
  "$BIN" ui >> "$LOG_FILE" 2>&1 &
else
  log "Running native binary"
  "$BIN" >> "$LOG_FILE" 2>&1 &
fi
APP_PID="$!"
APP_STARTED=1
echo "$APP_PID" > "$PID_FILE"
wait "$APP_PID"
WAIT_STATUS="$?"
log "Native Spotify Remote exited with status $WAIT_STATUS"
exit "$WAIT_STATUS"
