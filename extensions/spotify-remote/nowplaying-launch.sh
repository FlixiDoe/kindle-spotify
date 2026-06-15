#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/nowplaying.pid"
BIN="$APP_DIR/bin/spotify-remote-arm"

mkdir -p "$APP_DIR/logs" "$APP_DIR/data"

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

if [ -f "$PID_FILE" ]; then
  OLD_PID="$(cat "$PID_FILE" 2>/dev/null)"
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    log "Now Playing loop already running as PID $OLD_PID"
    exit 0
  fi
fi

log "Starting native Now Playing UI"
nohup "$BIN" ui >> "$LOG_FILE" 2>&1 &
echo "$!" > "$PID_FILE"
exit 0
