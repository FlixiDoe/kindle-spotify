#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$APP_DIR/bin/spotify-remote-arm"
BIN_NEW="$APP_DIR/bin/spotify-remote-arm.new"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
STATUS_FILE="$APP_DIR/data/status.txt"

mkdir -p "$APP_DIR/data" "$APP_DIR/logs"

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

if [ -f "$BIN_NEW" ]; then
  chmod 755 "$BIN_NEW" >/dev/null 2>&1 || true
fi
if [ -f "$BIN" ]; then
  chmod 755 "$BIN" >/dev/null 2>&1 || true
fi

if [ -x "$BIN_NEW" ]; then
  BIN="$BIN_NEW"
fi

if [ ! -x "$BIN" ]; then
  log "ERROR: KUAL binary missing or not executable: $BIN"
  {
    echo "SPOTIFY REMOTE"
    echo "Binary missing or not executable."
    echo "$BIN"
    echo "Deploy or chmod the native binary."
  } > "$STATUS_FILE"
  if command -v eips >/dev/null 2>&1; then
    eips 2 0 "Spotify Remote"
    eips 4 0 "Binary missing or not executable."
  fi
  exit 1
fi

cd "$APP_DIR" || exit 1
log "Running KUAL action via $BIN: $*"
"$BIN" kual "$@" >> "$LOG_FILE" 2>&1
