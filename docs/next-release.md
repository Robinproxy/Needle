# v0.7.16 — Cost display ordering and exchange-rate logs

- Prevent an older in-flight HTTP metadata response from overwriting newer live
  cost visibility, display currency, exchange rates, or stale-rate indicators.
- Keep HTTP fallback updates working after SSE disconnects; invalid or closed-
  stream updates do not invalidate good pending metadata.
- Track cache reads separately from ECB synchronization. Retry cooldown ticks
  no longer report remote recovery; only a successful synchronization clears
  an exchange_rates failure.
- Preserve cached rates across failed or canceled attempts, retain existing
  fetch/retry intervals, and avoid recovery logs during shutdown.
- Add asynchronous frontend ordering tests and deterministic synchronization,
  cooldown, cache-failure, and cancellation regressions; update README.

Upgrade the Server and refresh the dashboard to apply these fixes. The Agent,
configuration fields, database schema, and billing calculations are unchanged.
