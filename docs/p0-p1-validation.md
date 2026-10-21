# P0/P1 implementation and validation

This record covers local implementation and validation. The user subsequently authorized committing and pushing the sources; an official tag, GitHub Release and production deployment still require separate authorization. Runtime gates below remain pending until their CI runs succeed.

1. Server identity, permissions, consistent backups, CLI and rollback.
2. Loopback defaults with preserved upgrades; container exposure separately.
3. Self-revocation without deleting monitoring data.
4. Transactional report deduplication before bounded Agent retries.
5. Boot and actual network scope identities with conservative migration.
6. Reproducible isolated load tests; evidence before optimization.
7. Release preparation and artifact validation, without publishing.

All seven implementation stages are complete locally. Real Linux/container
runtime gates remain pending CI; this document distinguishes them from executed
workstation checks. See the [changed file list](changed-files.md),
[load results](load-testing.md), [release notes](next-release.md) and
[publication gates](release-preparation.md).

## Stages 1–2

- Root installs; `needle-server` runs the binary with no capabilities, strict systemd filesystem protection and UMask 0077. Only the dedicated DB directory is writable. Root owns the executable, parents, environment and unit.
- Existing ExecStart (listen, DB, optional TLS flags) and environment are preserved. Unsupported drop-ins, shared data directories, symlinks and paths hidden by the sandbox stop migration with actionable errors. The installer never falls back to root.
- Stopped-service backup uses SQLite's backup API and quick_check, including committed WAL. Originals and backup stay root-only under `/opt/needle/recovery/migration.*`. Data permissions change only on the directory, database and known sidecars, without recursive chown.
- Before DB migration, failure restores executable/unit and recorded permissions. Once migration may have started, failure leaves the service stopped and preserves new DB state; no automatic DB restore or unsafe binary downgrade. Fix forward, or explicitly review schema compatibility and restore to a separate database for investigation. Never restore the old backup over newly received reports.
- Root Linux CLI re-executes as DB owner *before* opening SQLite. Legacy root deployments remain manageable before migration. Mismatched DB/directory owners fail explicitly.
- New native deployments default to `127.0.0.1:8008`; upgrades preserve original listener. Docker continues to listen on `:8008` inside the container and publishes on host loopback. UID 1000 is explicit; bind-mount data must already be owned by UID/GID 1000 with mode 700.
- Normal uninstall retains data, configuration and the identity needed to access it. Purge refuses custom data paths; installer-created users are removed only if the matching UID has no other files/processes. Reused users are retained.
- Executed: nine portable Server installer regressions, including live-WAL backup, legacy implicit wildcard listener preservation, saved custom DB detection before purge, and stop-failure retention. Real systemd migration, restart and root CLI/WAL regressions are provided by `scripts/test-server-systemd.py` and CI; they have **not run on this macOS host**.
- Installer and root CLI share a per-database administration lock. CLI re-exec uses the owner's verified non-root primary group, never group 0 inherited from a mis-owned file. Effective systemd sandbox properties are checked before migration/start and after start. Privileged listen ports are rejected before stopping the old service when empty capabilities would prevent binding; explicitly use a high port/reverse proxy.

## Stage 3: history-preserving revocation

`POST /api/unregister` checks the current credential/hostname binding inside the
revocation transaction and removes exactly that credential. It retains the node,
raw/hourly history, billing metadata, traffic records and other credentials.
Credential-specific realtime state and rate-limit entries are cleared. Subsequent
reports and repeat unregister return 401; wrong-binding and missing credentials
return the same generic response. A bound credential can still be revoked if its
node record is already missing. Report authorization is rechecked in the write
transaction, preventing a revoked credential from binding or committing later.

Linux/OpenWrt uninstall invokes the installed Agent's safe unregister operation.
It first requires an authenticated Server capability explicitly advertising
`unregister_retains_history: true`. Old binary/Server combinations are refused
for unregister; plain local uninstall remains available. No public deletion or
administrator API was added. Explicit Server-local `delete-agent` retains its
confirmation, with `-y` for authorized automation. `revoke-token` now also retains
monitoring records.

## Stage 4: protocol, queues and timestamps

- `GET /api/report-capabilities`, authenticated by Agent token, advertises
  `persistent-report-v1`, 86400 seconds of accepted sample age and safe revocation.
- Each full report has optional `report_id`: 32 lowercase hex process-instance
  characters plus `:` and a monotonic positive sequence. This random process ID
  changes on Agent restart and is separate from kernel boot identity.
- Server receipt namespace is `(agent_id, report_id)`. The digest covers validated
  business content, not the token. First credential binding, metrics, Ping,
  traffic, metadata and receipt commit together. Same-ID/same-content repeats
  acknowledge without business writes; different content returns 409. Token
  rotation for the same hostname/node can acknowledge an already committed ID.
- Success ACK contains `status`, `protocol`, `report_id` and `duplicate`, and is
  returned only after commit. Receipt records survive Server restart. Unknown
  identified samples older than 24h or over 5m in the future return 422; receipts
  are retained 48h, so cleanup cannot make an expired unknown report count again.
  Slightly fast clocks are capped at receipt time for stored samples/traffic;
  legitimate delayed samples retain their historical sampling time.
- New Agent first confirms Server protocol support. Read-only negotiation may
  retry transient failures; a Server without the protocol gets the original
  single-attempt report behavior and one explanatory log until Agent restart.
  An old Agent without IDs is accepted, without equivalent retry guarantees.
- One historical sender freezes the completed report and Ping results once.
  The FIFO contains at most 64 reports by default, **including in-flight/retry**,
  with an 8 MiB reservation budget and 1 MiB per report. Reservations include
  space for bounded asynchronous probes. When full, new samples are rejected;
  the retrying head is retained. Realtime still keeps one latest-only item.
- Age is measured from original sample time, default 1800s. Backoff has jitter,
  starts around 1s and caps at 30s; Retry-After is also capped. Transient network
  failures and HTTP 408/429/500/502/503/504 retry within the age budget. Invalid
  configuration, permanent TLS/DNS errors, 409 and expired samples are discarded
  explicitly; 401/403 cause a cooldown, with no authentication bypass.
- Logs distinguish reservations/queue depth, oldest age, retries, queue-full,
  expiry/permanent failures and recovery. They do not include credentials or full
  report bodies. Process exit loses the memory queue; crash losses cannot be
  counted retrospectively. Shutdown cancellation is bounded.
- Realtime and history order remain independent. A later realtime update cannot
  prevent a valid earlier full sample from being saved, and older history cannot
  replace newer live state or roll traffic identity backward. Charts insert null
  points for obvious gaps (>3 expected sample/bucket intervals), with no line
  connecting them. Historical cadence changes remain a heuristic because sample
  intervals are not stored on each historical row.

Optional local Agent settings (neither needs a new ordinary installer prompt):

```yaml
realtime_interval: 5  # fresh default; omitted old configs keep extra realtime off
interval: 30         # full history/Ping/traffic; independently configurable
# history_queue_capacity: 64          # 1–1024, also bounded by 8 MiB
# history_retry_max_age_seconds: 1800 # 1–86400, original sample age
```

`-check-config` validates these settings and token header safety offline. Existing
YAML and tokens are retained by upgrade; explicitly configured zero for either
advanced history setting uses its default. No Agent disk spool was introduced.

## Stage 5: traffic state and database migration

Linux Agent reads `/proc/sys/kernel/random/boot_id` once per process; failure is
logged and stays missing. `network.network_scope_id` is SHA-256 over sorted names
and available ifindexes of interfaces actually counted, excluding counters.
`boot_id` is a top-level report field. Interface ordering and counter growth do
not change scope; replacement/removal can. Failed/empty collection preserves the
prior collector state, with missing metrics rather than fabricated zero counters.

| Input state | Accounting action |
|---|---|
| Same boot/scope, increasing counters | Add high-watermark difference |
| Same identity, counters fall temporarily | Keep high watermark; do not charge the full counter |
| Agent process restarts, kernel boot unchanged | Continue the same identity |
| New, plausible later boot identity | New epoch; count bytes since that boot |
| Same boot, changed interface scope | Rebase; existing interface counters are not newly used traffic |
| First strong identity on an existing legacy record | Preserve usage, rebase; do not charge startup counters |
| Missing identity after strong state exists | Freeze new usage, retain identity, log reason |
| Older sample / previously seen boot identity | Keep the current epoch; do not switch backward |
| No new identity ever supplied (old Agent) | Keep the existing uptime/high-watermark compatibility path |

The migration adds fields/tables in place and does not delete/rebuild traffic
tables or replay incomplete raw history. Existing accumulated usage is preserved.
Strong-state first migration intentionally omits the ambiguous transition delta.
Driver resets without identity changes can still lose traffic until counters
exceed the old high watermark. Neither boot IDs nor retries recover counters lost
before the last successful sample.

Payment/renewal dates and monthly reset rules remain unchanged. Monthly buckets
use the Server timezone and the expiry day-of-month (forever: day 1); long payment
periods do not change monthly reset frequency. Delayed samples use their sampled
cycle. A new cycle starts from its first valid counter baseline; gaps spanning a
boundary are not given a fabricated exact split. Older reports cannot reverse an
established global identity. Changing renewal anchors still uses existing cycle
transition protection. No automatic historic "correction" is run. Legacy traffic
repair refuses established strong-identity state and requires manual review.

| Database change | Purpose and lifetime |
|---|---|
| `report_receipts` + age index | Transactional report digest/ACK evidence; 48h retention |
| `traffic_baselines.boot_id`, `.network_scope_id` | Per-cycle identity with existing usage/high-watermark fields |
| `traffic_counter_identity` | One current identity/sample timestamp/reason per node |
| `traffic_boot_epochs` | Reject return to observed old boots; at most 64/node, old evidence cleaned after 48h except current boot |
| `admin_revision` | Persisted invalidation for external CLI revocation/deletion/legacy repair |

Existing legacy evidence, raw seven-day retention (whole-hour boundaries),
90-day hourly summaries and 62-day old traffic-cycle cleanup remain. Explicit
node deletion removes the new evidence tables too. Identified report windows are
independent of raw-history retention. Boot evidence is bounded: an ancient boot
outside retained evidence cannot be conclusively recognized if it returns with
a falsely newer clock; this is not complete per-interface accounting.

## Stage 6: measured performance and cache correctness

Executed 38 matrix/fault/stability cases plus two same-source controlled runs;
see [the full report and preserved results](load-testing.md). Cache reuse reduced
measured SQL by 27.1% in the controlled pair. The cache is bounded to 4096 nodes,
expires after 15s/local date changes and contains committed data only. Full
commits, node metadata changes, local admin revisions and schema changes invalidate
it. Credential checks are not bypassed; storage/schema failures remain errors.
No-subscriber broadcasts avoid unnecessary snapshot reads. Slow SSE consumers
do not hold report processing indefinitely. A global report lock and cache-miss
queries remain, with visible default-limit bottlenecks under concentrated load.

## Executed final checks

On this macOS host, `GOCACHE=/tmp/needle-go-cache
GOMODCACHE=/tmp/needle-go-mod bash scripts/check.sh` passed:

- `go test ./...`, `go test -race ./...`, `go vet ./...`.
- 83 portable Python regressions: Linux installer 8, OpenWrt installer 39, upgrade
  configuration 8, billing 5, intervals 5, TCP selection 5, Server installer 9 and
  archive safety 4.
- Five Node dashboard suites: billing, refresh recovery, realtime ordering/history,
  sparklines/hover and probe/history chart behavior.
- Installer and Docker-test shell syntax.

Go fault regressions cover commit-then-lost-ACK across Server restart, transaction
failure and binding rollback, content conflict, expiry after receipt cleanup,
token rotation, concurrent revocation/reporting, queue count/byte bounds,
authentication cooldown, transient negotiation, permanent errors, identity
migration/reboot/scope/regression/missing fields and stale epochs. Existing date,
rollup, deletion, dual reporting and independent sampler tests remain green.

Native candidate packages were built, extracted and **executed**: archive/path
safety, component hashes, version/commit, installer protocol, offline Agent YAML,
Server startup with temporary DB, and served `/app.js` hash all passed. Linux
amd64/arm64 static Agents were built and their ELF architecture/interpreter fields
checked. See [artifact checksums](artifact-validation.json). These are local dirty
candidates based on the base commit, not official binaries from a release commit.

## Remaining gates and safe rollout

Pending, explicitly **not locally passed**: real Linux Server/Agent systemd
migration/start/report/restart/uninstall; Docker health/bind/root-CLI-then-write;
Linux CGO Server amd64/arm64 archives, including QEMU execution. Workflows now gate
publication on these checks. A manual Release workflow only uploads Actions
artifacts; a separately authorized tag is required to publish. Longer soak runs
and representative Linux hardware performance remain additional evidence to
collect; the short workstation runs do not establish capacity.

After review and successful CI on one committed source revision:

1. Upgrade Server first with the installer packaged in that compatible release.
   The native installer stops the service and saves SQLite/WAL-consistent backup,
   environment, unit, binary and old permissions in root-only recovery storage.
2. Confirm service identity, listener, health, token inventory and existing node
   histories. Run a root CLI operation and verify subsequent reports still commit.
   Custom TLS/DB paths must pass preflight; do not loosen the whole directory tree.
3. Upgrade Agents with `upgrade --keep-config`. Existing tokens/config remain;
   optionally use `upgrade --configure-intervals` to enable/change live reporting.
   Confirm capability negotiation, subsequent reports and expected traffic totals.
4. For failures before DB migration, installer restores recorded executable/unit
   and permissions. Once migration may have begun it keeps the service stopped,
   preserves current DB/new data and recovery files, and requires repair forward.
   For investigation, restore the backup to a separate path. Never overwrite a
   newer DB or assume swapping the binary reverses schema/accounting changes.
5. Downgrade only after explicit compatibility review. Stop new Agents or disable
   their new retry sender before a protocol downgrade. Full rollback requires a
   consciously chosen consistent DB snapshot and may omit later reports; no
   automatic restore or unsupported rollback is promised.

Local implementation/validation made no production changes. The subsequent source push is user-authorized; no official tag or GitHub Release is created as part of that push. The existing main-branch Docker workflow can build and publish the image after its checks pass; that does not deploy a production service.
