#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"

mkdir -p "$APP_DIR/logs"

echo "$(date '+%Y-%m-%d %H:%M:%S') Manual UI recovery requested" >> "$LOG_FILE"

ps 2>/dev/null | grep '[s]potify-remote-arm' | while read PID REST; do
  echo "$(date '+%Y-%m-%d %H:%M:%S') Killing Spotify Remote PID $PID" >> "$LOG_FILE"
  kill "$PID" 2>/dev/null || true
done

rm -f "$APP_DIR/data/spotify-remote.pid" "$APP_DIR/data/launcher.pid" /tmp/spotify-remote.framework-stopped

start framework >> "$LOG_FILE" 2>&1 || true
lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true

if command -v eips >/dev/null 2>&1; then
  eips -c
  eips 3 0 "Spotify Remote recovered."
  eips 5 0 "Press Home or reopen KUAL."
fi

exit 0
