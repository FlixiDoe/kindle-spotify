#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$APP_DIR/bin/spotify-remote-arm"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
DATA_FILE="$APP_DIR/data/nowplaying.json"
PID_FILE="$APP_DIR/data/nowplaying.pid"
STOP_FILE="$APP_DIR/data/nowplaying.stop"

mkdir -p "$APP_DIR/logs" "$APP_DIR/data"

FBINK="true"
for p in /mnt/us/libkh/bin/fbink /mnt/us/koreader/fbink /mnt/us/extensions/MRInstaller/bin/KHF/fbink; do
  if [ -x "$p" ]; then
    FBINK="$p"
    break
  fi
done

log() {
  echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG_FILE"
}

draw() {
  if [ "$FBINK" != "true" ]; then
    "$FBINK" "$@" >> "$LOG_FILE" 2>&1 || true
  else
    shift 2>/dev/null || true
  fi
}

draw_text() {
  SIZE="$1"
  ROW="$2"
  TEXT="$3"
  [ -n "$TEXT" ] || TEXT=" "
  if [ "$FBINK" != "true" ]; then
    "$FBINK" -q -S "$SIZE" -m -y "$ROW" "$TEXT" >> "$LOG_FILE" 2>&1 || true
  fi
}

clip() {
  TEXT="$1"
  MAX="$2"
  if [ "${#TEXT}" -gt "$MAX" ]; then
    printf "%s..." "$(printf "%s" "$TEXT" | cut -c 1-"$((MAX - 3))")"
  else
    printf "%s" "$TEXT"
  fi
}

json_value() {
  KEY="$1"
  sed -n "s/.*\"$KEY\"[ ]*:[ ]*\"\\([^\"]*\\)\".*/\\1/p" "$DATA_FILE" | head -n 1
}

json_bool() {
  KEY="$1"
  sed -n "s/.*\"$KEY\"[ ]*:[ ]*\\(true\\|false\\).*/\\1/p" "$DATA_FILE" | head -n 1
}

json_num() {
  KEY="$1"
  sed -n "s/.*\"$KEY\"[ ]*:[ ]*\\([0-9][0-9]*\\).*/\\1/p" "$DATA_FILE" | head -n 1
}

echo "$$" > "$PID_FILE"
log "Now Playing loop running as PID $$"

# Let KUAL finish closing/redrawing before we paint our app screen.
sleep 4

while [ ! -f "$STOP_FILE" ]; do
  "$BIN" kual nowplaying >> "$LOG_FILE" 2>&1

  TITLE="$(clip "$(json_value title)" 30)"
  ARTIST="$(clip "$(json_value artist)" 34)"
  ALBUM="$(clip "$(json_value album)" 34)"
  PROGRESS="$(json_value progress)"
  DURATION="$(json_value duration)"
  PLAYING="$(json_bool is_playing)"
  VOLUME="$(json_num volume)"
  SHUFFLE="$(json_bool shuffle)"
  REPEAT="$(json_value repeat)"
  ERROR="$(clip "$(json_value error)" 36)"

  [ -n "$TITLE" ] || TITLE="Spotify Remote"
  [ -n "$ARTIST" ] || ARTIST="No track loaded"
  [ -n "$ALBUM" ] || ALBUM="Run Login/Status if needed"
  [ -n "$PROGRESS" ] || PROGRESS="0:00"
  [ -n "$DURATION" ] || DURATION="0:00"
  [ -n "$VOLUME" ] || VOLUME="?"
  [ -n "$REPEAT" ] || REPEAT="off"

  if [ "$PLAYING" = "true" ]; then
    PLAY_ICON="PAUSE"
  else
    PLAY_ICON="PLAY"
  fi

  if [ -n "$ERROR" ]; then
    TITLE="Needs attention"
    ARTIST="$ERROR"
    ALBUM="Use KUAL controls, then refresh"
  fi

  draw -k
  sleep 1

  draw_text 3 5   "SPOTIFY REMOTE"
  draw_text 2 17  "+====================================+"
  draw_text 2 20  "|                                    |"
  draw_text 2 23  "|            ALBUM COVER             |"
  draw_text 2 26  "|                                    |"
  draw_text 2 29  "|                                    |"
  draw_text 2 32  "+====================================+"

  draw_text 4 50  "$TITLE"
  draw_text 3 63  "$ARTIST"
  draw_text 2 73  "$ALBUM"

  draw_text 2 86  "===================================="
  draw_text 2 93  "$PROGRESS                    $DURATION"
  draw_text 4 105 "$PLAY_ICON"
  draw_text 2 122 "VOL $VOLUME   SHUF $SHUFFLE   REP $REPEAT"
  draw_text 1 -8  "Display only. Use KUAL controls or Touch Remote."
  draw_text 1 -4  "Refreshes every 8s. Stop via KUAL."

  sleep 8
done

log "Now Playing loop stopped"
rm -f "$PID_FILE" "$STOP_FILE"
exit 0
