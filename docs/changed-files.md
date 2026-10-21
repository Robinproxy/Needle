# Changed files

66 task files, relative to the repository root. Existing unrelated `.claude/` and `design/` assets are excluded and retained. After local validation, the user authorized committing and pushing these sources. No official tag or GitHub Release is included in that authorization.

See [validation and migration](p0-p1-validation.md) for behavior, [load testing](load-testing.md) for measurements, and [release preparation](release-preparation.md) for remaining gates.

## Deployment, installers and CI

- `.github/workflows/docker.yml` (modified)
- `.github/workflows/release.yml` (modified)
- `.github/workflows/server-security.yml` (new)
- `Dockerfile` (modified)
- `scripts/needle-agent-openwrt.sh` (modified)
- `scripts/needle-agent.sh` (modified)
- `scripts/needle-server.sh` (modified)
- `scripts/test-docker.sh` (new)
- `scripts/test-linux-agent-systemd.py` (modified)
- `scripts/test-openwrt-installer.py` (modified)
- `scripts/test-server-installer.py` (new)
- `scripts/test-server-systemd.py` (new)

## Agent collection, scheduling and transport

- `agent.yaml.example` (modified)
- `cmd/agent/config_check.go` (modified)
- `cmd/agent/main.go` (modified)
- `cmd/agent/runtime_test.go` (modified)
- `cmd/agent/schedule.go` (modified)
- `cmd/agent/schedule_test.go` (modified)
- `internal/agent/collector/boot_linux.go` (new)
- `internal/agent/collector/boot_other.go` (new)
- `internal/agent/collector/network.go` (modified)
- `internal/agent/collector/network_identity_test.go` (new)
- `internal/agent/history.go` (new)
- `internal/agent/history_test.go` (new)
- `internal/agent/reporter.go` (modified)
- `internal/agent/reporter_test.go` (modified)

## Server authentication, history, traffic and snapshots

- `cmd/server/cli_identity_linux.go` (new)
- `cmd/server/cli_identity_other.go` (new)
- `cmd/server/main.go` (modified)
- `internal/server/committed_cache.go` (new)
- `internal/server/committed_cache_baseline.go` (new)
- `internal/server/committed_cache_mode.go` (new)
- `internal/server/committed_cache_test.go` (new)
- `internal/server/diagnostics.go` (new)
- `internal/server/diagnostics_trace.go` (new)
- `internal/server/handler.go` (modified)
- `internal/server/history_rollup.go` (modified)
- `internal/server/model.go` (modified)
- `internal/server/realtime.go` (modified)
- `internal/server/report.go` (modified)
- `internal/server/report_receipts.go` (new)
- `internal/server/report_receipts_test.go` (new)
- `internal/server/store.go` (modified)
- `internal/server/store_transaction_test.go` (modified)
- `internal/server/traffic_accounting.go` (modified)
- `internal/server/traffic_identity_test.go` (new)
- `internal/server/traffic_repair.go` (modified)
- `internal/server/unregister_test.go` (new)

## Frontend

- `internal/server/static/app.js` (modified)
- `scripts/test-realtime-dashboard.cjs` (modified)

## Load testing and release tools

- `cmd/needle-load/main.go` (new)
- `scripts/check.sh` (new)
- `scripts/package-release.py` (new)
- `scripts/run-load-matrix.py` (new)
- `scripts/summarize-load.py` (new)
- `scripts/test-release-tools.py` (new)
- `scripts/verify-release.py` (new)

## Documentation and recorded evidence

- `README.md` (modified)
- `docs/artifact-validation.json` (new)
- `docs/changed-files.md` (new)
- `docs/load-results/raw-results.json.gz` (new)
- `docs/load-results/summary.csv` (new)
- `docs/load-testing.md` (new)
- `docs/next-release.md` (new)
- `docs/p0-p1-validation.md` (new)
- `docs/release-preparation.md` (new)
