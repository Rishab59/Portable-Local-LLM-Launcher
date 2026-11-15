#!/usr/bin/env sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in x86_64|amd64) ARCH=x64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "Unsupported CPU architecture: $ARCH" >&2; exit 1 ;; esac
case "$OS" in
  linux) PLATFORM="linux-$ARCH" ;;
  darwin) PLATFORM="macos-$ARCH" ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac
CONTROLLER="$ROOT/Controller/$PLATFORM/portableai-controller"
RUNTIME="$ROOT/Runtime/$PLATFORM/llamafile-0.10.6"
if [ ! -x "$CONTROLLER" ]; then echo "Controller binary missing or not executable: $CONTROLLER" >&2; exit 1; fi
if [ ! -x "$RUNTIME" ]; then echo "Runtime binary missing or not executable: $RUNTIME" >&2; echo "Place a compatible llamafile-0.10.6 binary there manually. No download will be attempted." >&2; exit 1; fi
mkdir -p "$ROOT/Models" "$ROOT/Logs" "$ROOT/Config"
"$CONTROLLER" --root "$ROOT" >> "$ROOT/Logs/controller-console.log" 2>&1 &
PID=$!
printf '%s\n' "$PID" > "$ROOT/Logs/controller.pid"
if command -v curl >/dev/null 2>&1; then
  for i in $(seq 1 40); do
    if curl -fsS http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
      case "$OS" in linux) command -v xdg-open >/dev/null 2>&1 && xdg-open http://127.0.0.1:8787/ >/dev/null 2>&1 || true ;; darwin) open http://127.0.0.1:8787/ >/dev/null 2>&1 || true ;; esac
      echo "PortableAI is running at http://127.0.0.1:8787/"
      echo "Use the control panel Exit button to stop owned model servers and the controller."
      exit 0
    fi
    sleep 0.25
  done
  echo "Controller did not respond on port 8787. Check Logs/controller.log; no process was killed." >&2
  exit 1
fi
sleep 1
case "$OS" in linux) command -v xdg-open >/dev/null 2>&1 && xdg-open http://127.0.0.1:8787/ >/dev/null 2>&1 || true ;; darwin) open http://127.0.0.1:8787/ >/dev/null 2>&1 || true ;; esac
echo "Attempted to open http://127.0.0.1:8787/. If it is unavailable, check Logs/controller.log; no process was killed."
