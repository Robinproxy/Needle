# Isolated load testing

These are measured workstation results, not a supported node-count guarantee.
All runs used new temporary SQLite databases, synthetic credentials and loopback
HTTP. No real database, Agent, VPS or third-party Ping endpoint was used.

## Environment and provenance

- Run date: 2026-10-20, America/Los_Angeles.
- Apple M3, 8 logical CPUs, 16 GiB RAM; macOS 26.6.2 (25G83), arm64.
- Go 1.26.4, CGO SQLite; trace-enabled child Server, separate load-generator process.
- Base commit: `a3f485ef60fa5fb2becd525628141166436898a2`, with local changes.
- Matrix source ID: `a3f485e-dirty-0f83e799e1e662b9d20daf40dc6035fdce7feab06fde4a2c820f980305af93dd`.
- Controlled comparison source ID: `a3f485e-dirty-714707c2cb901cd868dce06cf673757e37302c07483225b6f28f4d844f77ac17`, with `-baseline` / `-cached` suffixes.

Each case starts an empty, disposable child DB and seeds one metric per node
before measurement. Each full report contains three synthetic Ping results.
Realtime is every 5 seconds; full history is every 30 seconds. Default HTTP/SSE
limits are retained. No limits were increased to make the results look better.

The 36-case matrix covers 100/500/1000 nodes × 0/1/8/32 SSE clients ×
spread/burst/recovery, 35 seconds each. Additional cases cover faults and a slow
reader for 65 seconds, and 1000 nodes / 32 SSE clients for 180 seconds. The two
controlled comparison cases add another 70 seconds. All 40 JSON results are
preserved in [raw-results.json.gz](load-results/raw-results.json.gz); the derived
[summary.csv](load-results/summary.csv) contains every case and resource metric.

The matrix was run before the final atomic first-token binding, stricter error
classification and installer finishing changes. The controlled comparison uses
the later same-source pair. Subsequent changes are covered by functional tests;
the matrix is not misrepresented as a run of a future release commit. The final
tool now runs initial/hourly maintenance; the earlier matrix omitted that timer.
These new databases and short cases do not exercise large historical cleanup.

## Selected results

H denotes history. Counts are HTTP attempts, so a retry can add an attempt without
adding another unique report. Status 0 means a client transport failure or
cancellation. Resource peaks below include only the child Server, not the load
generator. CPU uses 100% for one logical core.

| Case | Realtime status counts | H acknowledged / created | H retry attempts | Peak RSS | H attempt p95 |
|---|---|---:|---:|---:|---:|
| 1000 / 32 SSE, spread, 35s | 200: 6922; 429: 8; 503: 69 | 1167 / 1167 | 0 | 51.7 MiB | 5.30 ms |
| 1000 / 32 SSE, burst, 35s | 200: 581; 503: 6419 | 1600 / 2000 | 3800 | 146.2 MiB | 100.64 ms |
| 1000 / 32 SSE, recovery, 35s | 200: 6918; 429: 14; 503: 67 | 1167 / 1167 | 966 | 55.6 MiB | See CSV |
| 1000 / 32 SSE + slow reader, faults, 65s | 200: 12854; 401: 8; 429: 34; 503: 103 | 2165 / 2167 | 1266 | 55.9 MiB | 4.37 ms |
| 1000 / 32 SSE, spread, 180s | 200: 35514; 429: 174; 503: 307 | 6000 / 6000 | 0 | 56.8 MiB | 4.62 ms |

The burst case had 3800 history 429 responses and 400 transport cancellations at
test end: 400 generated reports were not acknowledged or committed. Its maximum
sample-to-ACK time was 15.23s. Accepted SSE connections rose from 32 initial readers
to 64 total, indicating reconnects; reader p95 event delay reached 7.20s. The
global report lock, token limits, SSE write pressure and admission limits remain
constraints. This is not an all-green 1000-node capacity result.

The fault case injected a four-second DB write lock, discarded acknowledgements
after commit and revoked synthetic node 0's credential. Its history attempt
counts were 200: 3165, 401: 2, 500: 266; 2165 distinct reports committed. The two
failed reports belong to the revoked credential. One extra SSE reader was denied
with 503 at the configured 32-client limit. The maximum sample-to-ACK time was
15.02s; it includes synthetic-client waiting/backoff, not Agent sampling time.

For all 40 cases, metric row growth equals receipt row growth, and Ping row growth
equals three times receipt row growth: **zero excess metric or Ping rows** in the
measured final snapshots. Specific unit regressions separately prove that lost
ACK/retry does not duplicate traffic, failed-Ping denominators or metadata writes.
This row-count check alone is not a proof of every possible failure scenario.

The 180s run ended with 6000 unique commits, 3.37 MB DB and 4.21 MB WAL; peak CPU
66.1%, 99 goroutines and 76 FDs. It is a short stability observation, not evidence
of a 24-hour soak or long-term memory stability. Full p50/p95/p99, CPU/RSS/FD,
database sizes and status counts for every case are in the CSV and raw JSON.

## Controlled optimization comparison

Both binaries came from the same source snapshot. The `needle_load_baseline`
build tag disables only committed snapshot reuse and the no-subscriber shortcut;
this compares those optimizations, not an old production release to the new one.
Cases ran sequentially under quiet local conditions: 1000 nodes, 1 SSE client,
spread arrivals, 35s, identical defaults and fresh databases.

| Measurement | Baseline | Cached |
|---|---:|---:|
| Measured SQL statements, excluding seed | 145215 | 105800 |
| Realtime p95 | 2.028 ms | 1.837 ms |
| History p95 | 3.471 ms | 3.563 ms |
| H unique commits / generated | 1167 / 1167 | 1167 / 1167 |
| Realtime HTTP 429 | 19 | 18 |
| Peak RSS | 37.6 MiB | 39.9 MiB |
| SSE unexpected disconnects | 0 | 0 |

SQL statements fell by 27.1% in this pair. Latency differences from one short pair
are not statistically established improvements. The cache stores committed
metric/Ping/traffic data only, is bounded and expires, and is invalidated by
report commits, admin revisions, metadata changes and schema changes. Token
validation still accesses the DB. The global full-report lock remains; DB access
was reduced, not eliminated.

## Measurement limits

- The generator has one in-flight historical report per synthetic sender and
  uses closed-loop retries. It does not simulate a real Agent's 64-slot queue,
  independent CPU sampling, memory use or collection work. Those are tested
  separately by Agent/scheduler regressions.
- HTTP latency is per client attempt; sample-to-ACK includes backoff. SSE delay
  ends at the synthetic reader, not browser paint. Neither is CPU sampling wait.
- Exact SQLite lock-wait time is unavailable and recorded as null. SQL profile
  time includes query execution; DB pool wait is a different metric. Injected
  lock errors surfaced as HTTP 500 even where the optional SQLite busy trace
  counter stayed zero, so zero trace busy counts do not mean zero contention.
- Original matrix `sse_disconnects` includes normal test-end closes. It cannot
  distinguish all unexpected disconnects. The later tool and final pair separate
  `sse_closed_at_test_end`; retain this distinction when comparing results.
- Final snapshots are sampled after senders finish/cancel and a short drain.
  An unacknowledged report could have committed in a different failure scenario;
  compare receipt rows as well as acknowledgements rather than assuming equality.

## Reproduce

Only build/run these against disposable local resources. The tool has no remote
target option and refuses an existing child database. Build with the intended
source ID rather than describing a dirty tree as a clean commit.

```sh
go build -tags sqlite_trace -ldflags="-X main.SourceID=YOUR_REVIEWED_SOURCE_ID" -o /tmp/needle-load ./cmd/needle-load
python3 scripts/run-load-matrix.py /tmp/needle-load /tmp/needle-load-results
python3 scripts/summarize-load.py /tmp/needle-load-results /tmp/needle-load-summary

# Longer stability check, not yet executed for this task:
/tmp/needle-load -nodes 1000 -sse 32 -duration 30m -pattern spread > /tmp/needle-soak.json

# Same-source controlled baseline:
go build -tags 'sqlite_trace needle_load_baseline' -ldflags="-X main.SourceID=YOUR_REVIEWED_SOURCE_ID-baseline" -o /tmp/needle-load-baseline ./cmd/needle-load
/tmp/needle-load-baseline -nodes 1000 -sse 1 -duration 35s -pattern spread > /tmp/before.json
/tmp/needle-load -nodes 1000 -sse 1 -duration 35s -pattern spread > /tmp/after.json
```

`scripts/summarize-load.py` preserves the 36 matrix and two extra cases plus optional
`final-before.json` / `final-after.json` files. Keep its raw archive alongside the
CSV so calculated rates and row counts can be independently checked.
