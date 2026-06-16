#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
PID_FILE="$APP_DIR/data/nowplaying.pid"
FLAG_FILE="/tmp/spotify-remote.framework-stopped"

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

ps 2>/dev/null | grep '[s]potify-remote-arm ui' | while read PID REST; do
  kill "$PID" 2>/dev/null || true
done

ps 2>/dev/null | grep '[s]h .*/nowplaying.sh' | while read PID REST; do
  kill "$PID" 2>/dev/null || true
done

ps 2>/dev/null | grep '[s]h .*/run-native.sh' | while read PID REST; do
  kill "$PID" 2>/dev/null || true
done

if [ -f "$FLAG_FILE" ]; then
  echo "$(date '+%Y-%m-%d %H:%M:%S') Restarting framework from Now Playing stop" >> "$LOG_FILE"
  rm -f "$FLAG_FILE"
  if command -v start >/dev/null 2>&1; then
    start framework >> "$LOG_FILE" 2>&1 || true
  fi
  if [ -x /etc/init.d/framework ]; then
    /etc/init.d/framework start >> "$LOG_FILE" 2>&1 || true
  fi
  lipc-set-prop com.lab126.powerd preventScreenSaver 0 >/dev/null 2>&1 || true
  lipc-set-prop com.lab126.appmgrd start app://com.lab126.booklet.home >/dev/null 2>&1 || true
fi

exit 0
