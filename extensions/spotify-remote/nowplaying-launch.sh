#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"

mkdir -p "$APP_DIR/logs"
echo "$(date '+%Y-%m-%d %H:%M:%S') Queueing Now Playing screen" >> "$LOG_FILE"

nohup sh "$APP_DIR/nowplaying.sh" >> "$LOG_FILE" 2>&1 &
exit 0
