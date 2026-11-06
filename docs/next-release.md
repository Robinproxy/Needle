# v0.7.11 — Go security update

- Build Server and Agent releases with Go 1.27.3, including the standard-library
  security fixes for GO-2026-6609 and subsequent certificate validation fixes.
- Pin the Docker builder to the same Go version and reject implicit toolchain
  downloads inside the builder.
- Gate binary and Docker publication on a pinned govulncheck scan.
- Add a regression test for excessive HTTP Range requests while preserving
  normal partial-content responses.
- Add the Manage display option to hide the dashboard monthly cost summary.

Rebuild and upgrade existing Server and Agent installations to apply the Go
security fixes. Agent configuration and Tokens remain unchanged.
