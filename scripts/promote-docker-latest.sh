#!/usr/bin/env bash
# CI-only: the calling job must hold the shared latest-promotion concurrency lock.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo 'Usage: promote-docker-latest.sh COMMIT IMAGE@sha256:DIGEST' >&2
  exit 2
fi
source_commit=$1
source_image=$2
repository=${NEEDLE_IMAGE:-ghcr.io/robinproxy/needle}
if [[ ! "$source_commit" =~ ^[0-9a-f]{40}$ ]] ||
   [[ "${source_image%@*}" != "$repository" ]] ||
   [[ ! "${source_image##*@}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo 'Invalid source commit or image digest; latest was not changed.' >&2
  exit 1
fi
if [ "${GITHUB_REF:-}" != refs/heads/main ]; then
  echo 'Skip latest: only main builds can promote an image.'
  exit 0
fi

# Check after acquiring the workflow lock, immediately before the registry write.
# Failure to read main must fail closed, never publish on a guessed branch state.
remote_main=$(git ls-remote --exit-code origin refs/heads/main)
if [[ ! "$remote_main" =~ ^([0-9a-f]{40})[[:space:]]refs/heads/main$ ]]; then
  echo 'Cannot determine current main commit; latest was not changed.' >&2
  exit 1
fi
current_main=${BASH_REMATCH[1]}
if [ "$source_commit" != "$current_main" ]; then
  echo "Skip latest: $source_commit is no longer main ($current_main)."
  exit 0
fi

# Copy the tested manifest by digest; do not resolve a mutable candidate tag.
docker buildx imagetools create --prefer-index=false --tag "$repository:latest" "$source_image"
