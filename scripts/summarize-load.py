#!/usr/bin/env python3
"""Preserve synthetic load JSON and derive a compact, reproducible CSV."""
import argparse, csv, gzip, json, pathlib

p = argparse.ArgumentParser()
p.add_argument('input', type=pathlib.Path)
p.add_argument('output', type=pathlib.Path)
a = p.parse_args()
cases = {str(f.relative_to(a.input)): json.loads(f.read_text())
         for f in sorted((a.input/'matrix').glob('*.json'))}
for name in ('final-before.json', 'final-after.json'):
    if (a.input/name).exists():
        cases[name] = json.loads((a.input/name).read_text())
if not cases:
    raise SystemExit('No load result files found')
rows = []
for name, d in cases.items():
    samples = d['server_samples']
    b, end = d['server_baseline'], samples[-1]
    delta = lambda key: end[key]-b[key]
    peak = lambda key: max((x.get(key, 0) for x in samples), default=0)
    row = dict(case=name, source_id=d['source_id'], nodes=d['nodes'], sse=d['sse_clients'],
               pattern=d['pattern'], duration=d['duration'], history_created=d['history_created'],
               history_acked=d['history_acked'], history_failed_or_cancelled=d['history_failed_or_cancelled'],
               unique_commits=delta('report_receipts_rows'), metric_rows=delta('metrics_rows'),
               ping_rows=delta('tcpping_results_rows'),
               excess_metric_rows=delta('metrics_rows')-delta('report_receipts_rows'),
               excess_ping_rows=delta('tcpping_results_rows')-3*delta('report_receipts_rows'),
               retries=d['retry_attempts'], sender_pending_max=d['history_single_sender_pending_max'],
               sample_to_ack_max_ms=d['max_client_sample_to_ack_ms'],
               cpu_peak_one_core_100=round(peak('cpu_percent_one_core_100'), 3),
               rss_peak_bytes=peak('rss_bytes'), goroutines_peak=peak('goroutines'),
               fd_peak=peak('file_descriptors'), db_end_bytes=end['db_bytes'], wal_end_bytes=end['wal_bytes'],
               sql_statements=delta('sql_statements'), sql_profile_ns=delta('sql_profile_ns'),
               db_pool_wait_ns=delta('db_pool_wait_ns'), sqlite_busy_or_locked=delta('sqlite_busy_or_locked'),
               sqlite_lock_wait_ns=end['sqlite_lock_wait_ns'],
               sse_connections=d['sse_connections'], sse_disconnects_raw=d['sse_disconnects'],
               sse_closed_at_test_end=d.get('sse_closed_at_test_end', 'not_separated'))
    for endpoint, values in d['status'].items():
        count=sum(values.values())
        prefix=endpoint.rsplit('/',1)[-1]
        row[prefix+'_attempts']=count
        row[prefix+'_status_counts']=json.dumps(values,sort_keys=True,separators=(',',':'))
        row[prefix+'_2xx_percent']=round(100*sum(v for k,v in values.items() if k.startswith('2'))/count,4) if count else 0
    for label, values in {**d['latency'], 'sse_reader':d['sse_event_delay']}.items():
        for q in ('p50_ms','p95_ms','p99_ms'):
            row[label+'_'+q]=values.get(q)
    rows.append(row)
a.output.mkdir(parents=True,exist_ok=True)
fields=list(dict.fromkeys(key for row in rows for key in row))
with (a.output/'summary.csv').open('w',newline='') as f:
    writer=csv.DictWriter(f,fieldnames=fields,lineterminator='\n');writer.writeheader();writer.writerows(rows)
with gzip.open(a.output/'raw-results.json.gz','wt',encoding='utf-8') as f:
    json.dump(cases,f,separators=(',',':'))
print(f'Preserved {len(cases)} cases; excess metric/Ping rows: '
      f'{sum(r["excess_metric_rows"] for r in rows)}/{sum(r["excess_ping_rows"] for r in rows)}')
