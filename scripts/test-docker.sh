#!/usr/bin/env bash
# Disposable loopback container and a dedicated temporary bind mount only.
set -euo pipefail
image=${1:?image required}
test_dir=$(mktemp -d)
container_id=
cleanup() {
  result=$?
  trap - EXIT
  if [ "$result" -ne 0 ]; then
    echo "Docker smoke test failed (exit status $result)" >&2
    if [ -n "$container_id" ]; then
      docker inspect --format 'state={{.State.Status}} health={{if .State.Health}}{{.State.Health.Status}}{{end}} ports={{json .NetworkSettings.Ports}}' "$container_id" >&2 || true
      docker logs "$container_id" >&2 || true
    fi
  fi
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
  sudo rm -rf -- "$test_dir" || true
  exit "$result"
}
trap cleanup EXIT

# Docker can assign a new ephemeral *host* port when the container restarts.
# A passing in-container HEALTHCHECK alone does not prove host reachability.
wait_for_host_health() {
  local phase=$1 state mapping attempt
  for attempt in $(seq 1 60); do
    state=$(docker inspect --format '{{.State.Health.Status}}' "$container_id")
    if [ "$state" = healthy ]; then
      mapping=$(docker port "$container_id" 8008/tcp)
      if [[ ! "$mapping" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
        echo "unexpected Docker port binding: $mapping" >&2
        return 1
      fi
      if curl -fsS --connect-timeout 2 --max-time 5 -o /dev/null "http://$mapping/api/health" 2>/dev/null; then
        port=$mapping
        echo "$phase ready at $port"
        return 0
      fi
    fi
    sleep 1
  done
  echo "$phase: container not healthy and reachable from host within 60 attempts (last state: $state)" >&2
  return 1
}

sudo install -d -o 1000 -g 1000 -m 700 "$test_dir/data"
container_id=$(docker run -d -p 127.0.0.1::8008 -v "$test_dir/data:/data" "$image")
wait_for_host_health 'initial start'
[ "$(docker exec "$container_id" id -u)" = 1000 ]
docker exec "$container_id" needle-server -db /data/needle.db allow-token docker-test-only
docker exec -u root "$container_id" needle-server -db /data/needle.db list-agents
curl -fsS -H 'Authorization: Bearer docker-test-only' -H 'Content-Type: application/json' --data '{"hostname":"docker-ci","uptime":100}' "http://$port/api/report"
docker exec "$container_id" needle-server -db /data/needle.db list-agents | grep -F docker-ci
sudo find "$test_dir/data" -type f -exec stat -c '%u:%g:%a' {} \; | while read -r actual; do
  [ "$actual" = '1000:1000:600' ] || { echo "unexpected DB/sidecar ownership: $actual" >&2; exit 1; }
done
docker restart "$container_id" >/dev/null
wait_for_host_health 'after restart'
echo 'PASS Docker non-root, loopback publication, health, bind permissions, root CLI then write, restart'
