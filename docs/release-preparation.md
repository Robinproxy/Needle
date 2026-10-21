# Release preparation (commands are not executed)

Public latest release checked on 2026-10-20: [v0.7.5](https://github.com/Robinproxy/Needle/releases/tag/v0.7.5), commit `aaa0a6a`.
The working branch is `main`, based on `a3f485ef60fa5fb2becd525628141166436898a2`.
The user has authorized committing and pushing this change after local validation.
No official release/tag is selected. Do not infer the next unused tag from this
snapshot; check again immediately before selecting a release version.

## Gates before choosing a version

1. Review the complete local change list and [migration/validation record](p0-p1-validation.md).
2. Commit the reviewed sources, installers, examples, frontend, DB migrations,
   workflows and [release notes](next-release.md) together. Do not include unrelated
   local design assets or personal tool configuration.
3. On a separately authorized branch push, run **Release** manually. Manual runs
   prepare Actions artifacts and do not create a GitHub Release. The workflow
   performs Go/race/vet, installers/frontend, real disposable Linux systemd
   lifecycles, Docker isolation, amd64/arm64 builds and archive execution (QEMU
   for arm64). Review all results before publishing.
4. Verify each archive checksum and manifest version/commit, static Agent ELF,
   offline configuration and packaged Server's embedded frontend. Match the
   container's `-version` commit and source revision as well.
5. Recheck the latest Release and remote tags. Only after separate authorization,
   select an unused version and push a tag on the exact verified commit.

Read-only inspection / preparation commands after a reviewed branch push:

```sh
git status --short
git diff --check
gh release view --repo Robinproxy/Needle --json tagName,targetCommitish,publishedAt
git ls-remote --tags origin
gh workflow run release.yml --ref YOUR_REVIEWED_BRANCH
gh run list --workflow release.yml --limit 5
# Download the successful run's release-amd64 and release-arm64 Actions artifacts.
# sha256sum -c checksums-amd64.txt
# sha256sum -c checksums-arm64.txt
```

Commands reserved for later explicit publication authorization:

```sh
# Assign the user-approved unused version and the verified commit first.
# git tag -a "$NEXT_VERSION" "$TARGET_COMMIT" -m "$NEXT_VERSION"
# git push origin "refs/tags/$NEXT_VERSION"
```

Tag-triggered Release only publishes after all checks and both verified platform
artifacts succeed; it merges and validates checksums before creating the Release.
Docker publishing runs its own source/lifecycle/container gates. Native Server
requires CGO/SQLite; do not replace its Linux artifact with a CGO-disabled stub.

On this macOS host, native candidate archives were actually executed and checked,
and Linux amd64/arm64 Agent binaries were built and checked as static ELF. Linux
Server CGO archive execution, real systemd and Docker are **pending CI**, not
claimed as locally passed. No official version/tag, Release or deployment was
created during local validation. A source push to main triggers the existing
Docker workflow, which may publish an image after its checks pass; it does not
create a GitHub Release or deploy a production service.
