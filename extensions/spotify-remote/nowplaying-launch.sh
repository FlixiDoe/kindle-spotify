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
    log "Killing old Now Playing PID $OLD_PID"
    kill "$OLD_PID" 2>/dev/null || true
  fi
fi

ps 2>/dev/null | grep '[s]h .*/nowplaying.sh' | while read PID REST; do
  log "Killing old shell Now Playing loop PID $PID"
  kill "$PID" 2>/dev/null || true
done
ps 2>/dev/null | grep '[s]potify-remote-arm ui' | while read PID REST; do
  log "Killing old native UI PID $PID"
  kill "$PID" 2>/dev/null || true
done

log "Starting native Now Playing UI via framework-safe launcher"
nohup sh "$APP_DIR/run-native.sh" ui >> "$LOG_FILE" 2>&1 &
echo "$!" > "$PID_FILE"
exit 0
