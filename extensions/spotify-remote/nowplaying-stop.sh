#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/nowplaying.pid"

mkdir -p "$APP_DIR/logs" "$APP_DIR/data"
echo "$(date '+%Y-%m-%d %H:%M:%S') Stopping Now Playing loop" >> "$LOG_FILE"
touch "$APP_DIR/data/nowplaying.stop"

if [ -f "$PID_FILE" ]; then
  PID="$(cat "$PID_FILE" 2>/dev/null)"
  if [ -n "$PID" ]; then
    kill "$PID" 2>/dev/null || true
  fi
  rm -f "$PID_FILE"
fi

exit 0
