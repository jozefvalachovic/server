#!/usr/bin/env bash
set -euo pipefail

: "${HTTP_HOST:=127.0.0.1}"
: "${HTTP_PORT:=8080}"
: "${ADMIN_NAME:=admin}"
: "${ADMIN_SECRET:=secret}"
: "${ADMIN_SIGNING_KEY:=signing-key-change-me-1234567890}"

export HTTP_HOST HTTP_PORT DEV=1 ADMIN_NAME ADMIN_SECRET ADMIN_SIGNING_KEY

cd "$(dirname "$0")"

exec go run ./example
