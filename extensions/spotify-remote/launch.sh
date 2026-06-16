#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/launcher.pid"

mkdir -p "$APP_DIR/data" "$APP_DIR/logs"

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

if [ -f "$PID_FILE" ]; then
  OLD_PID="$(cat "$PID_FILE" 2>/dev/null)"
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    log "Launcher already running as PID $OLD_PID"
    exit 0
  fi
fi

log "Queueing native Spotify Remote launch"
nohup sh "$APP_DIR/run-native.sh" ui >> "$LOG_FILE" 2>&1 &
echo "$!" > "$PID_FILE"

if command -v eips >/dev/null 2>&1; then
  eips 3 0 "Spotify Remote starting..."
  eips 5 0 "Wait a few seconds."
fi

exit 0
