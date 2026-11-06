# Release checks

Use the reviewed source commit for every binary, installer, image, and archive.
Do not use historical validation snapshots as evidence for the current build.

Use the Go version in `go.mod` for release builds and keep the Docker builder's
Go version in sync. Its `GOTOOLCHAIN=local` setting rejects an outdated builder
instead of silently downloading a different compiler. Release and Docker CI run
the pinned `govulncheck` command below before building or publishing; a reachable
known vulnerability or a failed scan blocks publication. Rebuild and redeploy
existing binaries and images to apply Go standard-library security updates.

## Validation

1. Review `git status --short` and `git diff --check`; exclude unrelated local files.
2. Run `bash scripts/check.sh` for Go tests, race detection, vet, installer checks,
   and dashboard regressions. Update [release notes](next-release.md) for the
   changes since the last release.
3. After pushing the reviewed sources, dispatch the **Release** workflow on the
   intended branch. Manual runs upload candidate artifacts without creating a
   GitHub Release. Verify all workflow jobs, including disposable Linux systemd
   lifecycles, Docker permissions, both architectures, and archive execution.
4. Check archive checksums and manifest version/commit, static Agent ELF,
   configuration validation, and the packaged Server's embedded frontend.
   Check the container's `-version` source commit as well.

```sh
git status --short
git diff --check
GOTOOLCHAIN=local go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
bash scripts/check.sh
gh workflow run release.yml --ref YOUR_REVIEWED_BRANCH
gh run list --workflow release.yml --limit 5
```

## Publication

Check the latest release and remote tags before selecting a version. Create a
tag only when publication is requested, on the exact commit that passed review.

```sh
gh release view --repo Robinproxy/Needle --json tagName,targetCommitish,publishedAt
git ls-remote --tags origin
# Set the selected version and verified commit before tagging:
# git tag -a "$NEXT_VERSION" "$TARGET_COMMIT" -m "$NEXT_VERSION"
# git push origin "refs/tags/$NEXT_VERSION"
```

Tag-triggered Release publishes only after all checks and both platform builds
pass, merging and verifying checksums before creating the Release. Native Server
requires CGO/SQLite; do not replace it with a CGO-disabled build. Linux arm64
archives are executed using QEMU during validation.

A source push to `main` triggers Docker checks and image publication; it does not
create a GitHub Release or deploy a service. Real Linux systemd and Docker checks
run in CI and must not be reported as locally passed on macOS.

Docker builds publish only their own `ci-RUN_ID-RUN_ATTEMPT` candidate or version
tag. A separate main-only job promotes the tested image by digest to `latest`.
All promotions share a noncanceling concurrency group with pending jobs retained
using GitHub's [`queue: max`](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#concurrency);
inside that lock the guard rereads remote `main` and skips superseded commits.
Keep the group name stable across workflow revisions. Reordering, old reruns,
and version-tag builds using this workflow cannot roll back `latest`.

For the first rollout, cancel any queued or running Docker jobs using the old
workflow before publishing with the new policy. Archived workflow definitions
are not retroactively changed: do not rerun them or publish tags on pre-policy
commits without backporting the workflow fix. Rebuild using the current `main`
workflow instead. Registry publication and the GitHub concurrency queue still
require CI verification; local tests use disposable Git repos and a fake registry.
