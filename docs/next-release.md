# Dashboard refinements and configuration cleanup

- Move Manage to the dashboard header; simplify the footer to version and GitHub
  icon. Healthy connections no longer show a permanent Connected label; loading,
  stale data, connection errors, and Retry remain available.
- Add an optional Server-managed node note of up to 40 characters, shown beside
  uptime on cards. Empty notes are hidden and long notes truncate to fit.
- Fix filtering for nodes whose region has not been configured.
- Consolidate dashboard history range state and remove unused status fields,
  database statistics on `/api/info`, and test-only wrappers from production code.
- Reject extra Server CLI arguments instead of silently ignoring them.

## Agent configuration change

Region, billing currency/amount/period, renewal date, and traffic allowance now
belong to `/admin`. The Agent no longer accepts `region`, `billing_currency`,
`billing_amount`, `billing_period`, `expires_at`, or `monthly_traffic_limit_gb` in
YAML. Remove those fields before upgrading; retired keys produce a clear error.
The old `insecure` alias is also removed; use `tls_skip_verify` instead.

`server_managed` defaults to true; false is rejected. Fresh Agent installation
asks only for connection, identity, collection, and probe settings. Reconfiguration
changes probes and intervals without asking for billing or allowances. Kept
configuration and Token remain unchanged; validation failure retains the old
installation.

Upgrade the Server first and confirm the stored node settings. The first current
Agent report adopts an existing profile without clearing its saved billing,
region, traffic allowance, reset day, or accumulated usage. Existing stored dates,
traffic history, and migration protections are retained. No traffic accounting
or probe algorithm changes are included.

The dashboard changes and notes require only a Server upgrade. Agent configuration
cleanup takes effect when the new Agent binary is installed. No tag or release is
created by preparing these notes; see [release checks](release-preparation.md).
