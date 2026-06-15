#!/bin/sh
set -eu
ROOT="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$ROOT/bin"
CGO_ENABLED=0 GO111MODULE=off GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w" -o "$ROOT/bin/spotify-remote-arm" "$ROOT/src/native"
chmod 755 "$ROOT/bin/spotify-remote-arm"
echo "Built $ROOT/bin/spotify-remote-arm"
