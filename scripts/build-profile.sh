#!/usr/bin/env bash
set -euo pipefail
if [[ $# -ne 2 ]]; then
  echo 'Usage: scripts/build-profile.sh readonly|editor|full OUTPUT' >&2
  exit 2
fi
case "$1" in
  readonly|editor|full) ;;
  *) echo "Unknown profile: $1" >&2; exit 2 ;;
esac
go build -ldflags "-X i4dcli/internal/policy.embeddedProfile=$1" -o "$2" ./cmd/i4d
