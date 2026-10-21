#!/usr/bin/env bash
set -euo pipefail
go test ./...
go test -race ./...
go vet ./...
bash -n scripts/needle-server.sh
bash -n scripts/needle-agent.sh
bash -n scripts/test-docker.sh
sh -n scripts/needle-agent-openwrt.sh
for test in scripts/test-*.py; do
  case "$test" in *systemd.py) continue;; esac
  python3 "$test"
done
for test in scripts/test-*.cjs; do node "$test"; done
# Real Linux systemd / Docker / platform archive checks are separate CI gates.
