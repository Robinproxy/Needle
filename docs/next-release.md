# v0.7.15 — Docker publication ordering

- Separate tested image publication from latest promotion. Main builds publish
  a unique ci-RUN_ID-RUN_ATTEMPT candidate; tag builds publish only their version.
- Promote main images by immutable digest under one shared, noncanceling
  concurrency group that retains pending promotions.
- Recheck remote main immediately before promotion and skip superseded commits,
  preventing late builds and reruns using the new workflow from rolling back latest.
- Push only the intended image tag; remove the blanket --all-tags push.
- Add disposable-Git/fake-registry regressions, workflow wiring checks, and
  deployment documentation.

This release changes CI publication only; no Server or Agent upgrade is needed
for the ordering fix itself. Before the first rollout, stop queued/running Docker
jobs using the old workflow. Archived workflow definitions are not retroactively
updated; rebuild using the current main workflow instead of rerunning old ones.
