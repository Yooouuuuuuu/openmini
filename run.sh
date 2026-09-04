#!/bin/bash
# Starts openmini in a tmux session after checking the environment.
# Usage: ./run.sh [-y]   (-y skips the confirmation)
SESSION="openmini"
cd "$(dirname "$0")" || exit 1
export PATH="$HOME/.local/bin:$PATH"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1"; exit 1; }; }
need go; need tmux

if [ ! -f config.toml ]; then
    cp config.example.toml config.toml
    echo "config.toml did not exist; created it from config.example.toml."
    echo "Edit it (model, backends, api_keys), then run ./run.sh again."
    exit 1
fi

if [ ! -f openmini ] || [ -n "$(find . -name '*.go' -newer openmini 2>/dev/null | head -1)" ]; then
    echo "Building openmini..."
    go build -o openmini . || { echo "Build failed."; exit 1; }
fi

if tmux has-session -t "$SESSION" 2>/dev/null; then
    echo "Session $SESSION already exists.  Attach: tmux attach -t $SESSION   Stop: tmux kill-session -t $SESSION"
    exit 0
fi

echo "--- doctor ---"
./openmini doctor
rc=$?
echo "--------------"
if [ $rc -ne 0 ]; then
    echo "Fix the FAIL lines above, or continue anyway if you know why."
fi
if [ "$1" != "-y" ]; then
    read -r -p "Start openmini in tmux session '$SESSION'? [y/N] " ans
    case "$ans" in y|Y) ;; *) echo "Not started."; exit 0;; esac
fi

tmux new-session -d -s "$SESSION" -c "$PWD" -e "DISPLAY=${DISPLAY:-:0}" \
    './openmini serve; rc=$?; echo; echo "[openmini exited with status $rc] Press Enter to close."; read'
port=$(grep -E '^port *=' config.toml | head -1 | sed 's/[^0-9]//g'); port=${port:-18000}
echo "Started.  Attach: tmux attach -t $SESSION   Stop: tmux kill-session -t $SESSION"
echo "Base URL:  http://localhost:$port/v1"
if command -v tailscale >/dev/null 2>&1; then
    name=$(tailscale status --json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))' 2>/dev/null)
    [ -n "$name" ] && echo "Tailnet:   http://$name:$port/v1"
fi
echo "Quota:     ./openmini usage      State: ./openmini status"
