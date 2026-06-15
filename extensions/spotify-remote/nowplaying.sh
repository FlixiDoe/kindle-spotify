#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$APP_DIR/bin/spotify-remote-arm"
LOG_FILE="$APP_DIR/logs/spotify-remote.log"
DATA_FILE="$APP_DIR/data/nowplaying.json"

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

log "Rendering Now Playing screen"
"$BIN" kual nowplaying >> "$LOG_FILE" 2>&1

TITLE="$(json_value title)"
ARTIST="$(json_value artist)"
ALBUM="$(json_value album)"
PROGRESS="$(json_value progress)"
DURATION="$(json_value duration)"
PLAYING="$(json_bool is_playing)"
VOLUME="$(json_num volume)"
SHUFFLE="$(json_bool shuffle)"
REPEAT="$(json_value repeat)"
ERROR="$(json_value error)"

[ -n "$TITLE" ] || TITLE="Spotify Remote"
[ -n "$ARTIST" ] || ARTIST="No track loaded"
[ -n "$ALBUM" ] || ALBUM="Run Login/Status if needed"
[ -n "$PROGRESS" ] || PROGRESS="0:00"
[ -n "$DURATION" ] || DURATION="0:00"
[ -n "$VOLUME" ] || VOLUME="?"
[ -n "$REPEAT" ] || REPEAT="off"

if [ "$PLAYING" = "true" ]; then
  PLAY_ICON="PAUSE"
  STATE="NOW PLAYING"
else
  PLAY_ICON="PLAY"
  STATE="PAUSED"
fi

if [ -n "$ERROR" ]; then
  STATE="SPOTIFY REMOTE"
  TITLE="Needs attention"
  ARTIST="$ERROR"
  ALBUM="Use KUAL controls, then refresh this screen"
fi

# Let KUAL finish closing/redrawing before we paint our app screen.
sleep 2
draw -k
sleep 1

# A quiet centered layout inspired by a small dedicated music player.
# FBInk handles horizontal centering with -pmh; y values are text rows.
draw -q -pmh -y 2  "SPOTIFY REMOTE"
draw -q -pmh -y 5  "+============================+"
draw -q -pmh -y 6  "|                            |"
draw -q -pmh -y 7  "|        ALBUM  COVER        |"
draw -q -pmh -y 8  "|                            |"
draw -q -pmh -y 9  "|    refresh for now playing |"
draw -q -pmh -y 10 "|                            |"
draw -q -pmh -y 11 "+============================+"

draw -q -pmh -y 14 "$STATE"
draw -q -pmh -y 16 "$TITLE"
draw -q -pmh -y 18 "$ARTIST"
draw -q -pmh -y 20 "$ALBUM"

draw -q -pmh -y 23 "=============================="
draw -q -pmh -y 24 "$PROGRESS                         $DURATION"
draw -q -pmh -y 27 "|<          $PLAY_ICON          >|"
draw -q -pmh -y 30 "VOL $VOLUME   SHUFFLE $SHUFFLE   REPEAT $REPEAT"
draw -q -pmh -y -3 "KUAL controls: Play/Pause, Next, Previous"

exit 0
