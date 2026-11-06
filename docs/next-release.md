# v0.7.12 — Bounded reporting and history queries

- Authenticate full-report Tokens before reading request bodies; limit full
  reports to 16 concurrent requests and return retry hints when overloaded.
- Close rejected HTTP/1 requests without waiting for unread slow bodies.
  Keep transactional hostname binding and persistent report deduplication.
- Apply one history sampling policy to relative and explicit time windows,
  rejecting invalid, ambiguous, and overflowing parameters before querying.
- Preserve normal daily raw detail; aggregate dense results without dropping
  peaks or probe success/loss counts. Bound metric and probe response sizes.
- Limit history queries to four concurrent requests and ten seconds. Propagate
  browser cancellation to SQLite, including running aggregations.
- Add security regressions and document admission and history limits.

Upgrade the Server to apply these changes. Existing Agents do not need an
upgrade or configuration changes for this release. Tokens, stored history,
traffic accounting, and retention rules remain unchanged.
