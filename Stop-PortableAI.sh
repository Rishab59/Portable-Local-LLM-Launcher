#!/usr/bin/env sh
set -eu
if command -v curl >/dev/null 2>&1; then
  curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:8787/api/shutdown || { echo "PortableAI controller was not reachable on port 8787." >&2; exit 1; }
else
  echo "curl is unavailable. Use the Exit PortableAI button in the control panel." >&2
  exit 1
fi
