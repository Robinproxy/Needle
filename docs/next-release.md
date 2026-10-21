# Security, report reliability and traffic continuity

Upgrade **Server first, then Agents**. This release changes Server permissions,
reporting protocol and database metadata; take a consistent SQLite backup first.

- Native Server installs use a non-login `needle-server` user and a strictly
  limited writable database directory. New native installs listen on
  `127.0.0.1:8008`; upgrades preserve existing listen, database and TLS arguments.
  Container listening stays on `:8008`; Compose publishes on host loopback.
- Root CLI operations switch to the database owner before opening SQLite, keeping
  DB/WAL/SHM ownership compatible with the service. Environment, executable and
  service configuration remain root-managed.
- `/api/unregister` and `revoke-token` revoke only the specified credential. Node,
  billing, history and traffic records are retained. Only explicit local
  `delete-agent` removes those records, with confirmation (`-y` for automation).
  A new Agent refuses unregister against a Server that has not explicitly
  confirmed history-preserving semantics, avoiding the older deletion behavior.
- Full reports gain a process-instance/sequence identity and transactional,
  persistent Server deduplication. Retries require explicit protocol support and
  a matching committed acknowledgement. Duplicate reports never repeat metric,
  Ping or traffic writes; conflicting content returns 409.
- Agent history remains a bounded in-memory FIFO: capacity 64 including the
  in-flight report, at most 8 MiB reserved, default maximum age 1800 seconds from
  original sampling time. Advanced YAML settings are optional. Backoff and
  Retry-After are bounded; permanent errors are discarded explicitly. This does
  **not** survive Agent exit or guarantee unlimited offline backfill.
- Kernel boot identity and actual sorted interface-scope identity improve traffic
  continuity while retaining counter high-watermark protection. Initial migration
  preserves existing totals and rebases conservatively. A scope change does not
  charge existing counters. Missing identity after strong-state establishment
  freezes further traffic rather than guessing continuity. Kernel resets can
  still lose pre-reboot traffic; aggregate samples cannot precisely split gaps
  across monthly boundaries. No automatic historical correction is performed.
- Five-second realtime remains memory-only; default 30-second history, seven-day
  raw/day detail, 90-day hourly retention and Agent non-root isolation remain.
  History charts show gaps; committed snapshot reuse reduces repeated SQL reads.

## Upgrade and recovery

Keep old Agent YAML/token with `upgrade --keep-config`; optionally change only
reporting intervals later with `upgrade --configure-intervals`. Old configs omit
`realtime_interval` and keep extra realtime disabled. Defaults for fresh installs
remain 5 / 30. New optional settings are:

```yaml
history_queue_capacity: 64
history_retry_max_age_seconds: 1800
```

Native Server upgrades stop the service and use SQLite's consistent backup API,
including WAL, before migrating ownership/schema. Root-only backup and original
files are retained under `/opt/needle/recovery/migration.*`. Unsupported shared
paths, symlinks, custom unit/drop-ins, mounts or inaccessible TLS keys require an
explicit reviewed migration. The installer never falls back to root.

If deployment fails before database migration, original executable/unit and
recorded permissions are restored. Once migration may have begun, keep the
service stopped and repair forward. Binary rollback and database restore are
separate decisions: do not restore an old DB over reports already received, and
do not run an older Server against the new schema without reviewing compatibility.
Disable new Agent retries before any approved downgrade to a Server lacking the
persistent protocol. Prefer restoring a backup to an isolated path for review.

Use the installers bundled with the selected release. A newer main installer
checks its downloaded binary's protocol before touching the installation and
refuses an older incompatible release binary instead of generating unsupported
configuration.

## Validation and limits

See `docs/p0-p1-validation.md` for executed checks, migration details and remaining
validation gates; `docs/load-testing.md` describes isolated load runs. Workstation
load measurements are evidence about those exact runs, not a supported node-count
guarantee. Real Linux systemd, Docker and Linux release archive execution are CI
gates; local macOS builds do not substitute for those checks.
