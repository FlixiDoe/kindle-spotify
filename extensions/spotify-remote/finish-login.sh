#!/bin/sh
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
exec sh "$APP_DIR/run-kual.sh" finish-login
