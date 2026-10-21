#!/usr/bin/env bash
# Disposable loopback container and a dedicated temporary bind mount only.
set -euo pipefail
image=${1:?image required}
test_dir=$(mktemp -d)
container_id=
cleanup() {
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
  sudo rm -rf "$test_dir"
}
trap cleanup EXIT
sudo install -d -o 1000 -g 1000 -m 700 "$test_dir/data"
container_id=$(docker run -d -p 127.0.0.1::8008 -v "$test_dir/data:/data" "$image")
for i in $(seq 1 60); do
  state=$(docker inspect --format '{{.State.Health.Status}}' "$container_id")
  if [ "$state" = healthy ]; then break; fi
  sleep 1
done
[ "$state" = healthy ] || { docker logs "$container_id"; exit 1; }
[ "$(docker exec "$container_id" id -u)" = 1000 ]
port=$(docker port "$container_id" 8008/tcp)
case "$port" in 127.0.0.1:*) ;; *) echo 'port is not loopback-only' >&2; exit 1;; esac
curl -fsS "http://$port/api/health"
docker exec "$container_id" needle-server -db /data/needle.db allow-token docker-test-only
docker exec -u root "$container_id" needle-server -db /data/needle.db list-agents
curl -fsS -H 'Authorization: Bearer docker-test-only' -H 'Content-Type: application/json' --data '{"hostname":"docker-ci","uptime":100}' "http://$port/api/report"
docker exec "$container_id" needle-server -db /data/needle.db list-agents | rg docker-ci
sudo find "$test_dir/data" -type f -exec stat -c '%u:%g:%a' {} \; | while read -r actual; do
  [ "$actual" = '1000:1000:600' ] || { echo "unexpected DB/sidecar ownership: $actual" >&2; exit 1; }
done
docker restart "$container_id" >/dev/null
sleep 2
curl -fsS "http://$port/api/health"
echo 'PASS Docker non-root, loopback publication, health, bind permissions, root CLI then write, restart'
