#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
PID_FILE="$APP_DIR/data/spotify-remote.pid"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"

mkdir -p "$APP_DIR/logs"

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

start_framework() {
  if command -v start >/dev/null 2>&1; then
    start framework >> "$LOG_FILE" 2>&1 && return 0
  fi
  if [ -x /etc/init.d/framework ]; then
    /etc/init.d/framework start >> "$LOG_FILE" 2>&1 && return 0
  fi
  log "No framework start command available"
  return 1
}

if [ ! -f "$PID_FILE" ]; then
  log "Stop requested, no PID file present"
  if [ -f "$APP_DIR/data/launcher.pid" ]; then
    LPID="$(cat "$APP_DIR/data/launcher.pid" 2>/dev/null)"
    if [ -n "$LPID" ]; then
      log "Stopping launcher PID $LPID"
      kill "$LPID" 2>/dev/null || true
    fi
    rm -f "$APP_DIR/data/launcher.pid"
  fi
  ps 2>/dev/null | grep '[s]potify-remote-arm' | while read PID REST; do
    log "Stopping Spotify Remote process $PID"
    kill "$PID" 2>/dev/null || true
  done
  if [ -f /tmp/spotify-remote.framework-stopped ]; then
    log "Restarting framework after stop request"
    rm -f /tmp/spotify-remote.framework-stopped
    start_framework || true
    lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true
  fi
  exit 0
fi

PID="$(cat "$PID_FILE" 2>/dev/null)"
if [ -z "$PID" ]; then
  rm -f "$PID_FILE"
  log "Stop requested, empty PID file removed"
  exit 0
fi

if kill -0 "$PID" 2>/dev/null; then
  log "Stopping Spotify Remote PID $PID"
  kill "$PID" 2>/dev/null || true
  sleep 1
  if kill -0 "$PID" 2>/dev/null; then
    kill -9 "$PID" 2>/dev/null || true
  fi
else
  log "PID $PID is not running"
fi

rm -f "$PID_FILE"
if [ -f /tmp/spotify-remote.framework-stopped ]; then
  log "Restarting framework after stop request"
  rm -f /tmp/spotify-remote.framework-stopped
  start_framework || true
  lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true
fi
exit 0
