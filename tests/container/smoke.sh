#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary_directory="$(mktemp -d)"
project_name="dbgraph-container-test-$$"
compose=(
  docker compose
  --project-directory "$repository_root"
  --project-name "$project_name"
  --file "$repository_root/docker-compose.yml"
)
image_build=(
  docker build
  --file "$repository_root/Dockerfile"
  --tag dbgraph:container-test
)

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$temporary_directory"
}
trap cleanup EXIT INT TERM

export DBGRAPH_WEB_TOKEN="$(openssl rand -hex 32)"
export DBGRAPH_MCP_TOKEN="$(openssl rand -hex 32)"
export DBGRAPH_SECRET_KEY="$(openssl rand -hex 32)"
export DBGRAPH_PORT=0
export DBGRAPH_IMAGE="dbgraph:container-test"

"${compose[@]}" config --quiet
for proxy_variable in HTTP_PROXY HTTPS_PROXY NO_PROXY; do
  if [ -n "${!proxy_variable:-}" ]; then
    image_build+=(--build-arg "$proxy_variable")
  fi
done
if [ -n "${GOPROXY:-}" ]; then
  image_build+=(--secret id=go_proxy,env=GOPROXY)
fi
"${image_build[@]}" "$repository_root"
"${compose[@]}" up --detach --wait --no-build dbgraph

container_id="$("${compose[@]}" ps --quiet dbgraph)"
published_address="$("${compose[@]}" port dbgraph 8443)"

test -n "$container_id"
test "$(docker inspect --format '{{.Config.User}}' "$container_id")" = "65532:65532"
test "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$container_id")" = "true"
test "$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/dbgraph"}}{{.Type}}{{end}}{{end}}' "$container_id")" = "volume"

base_url="https://$published_address"
health="$(curl --fail --silent --show-error --insecure "$base_url/healthz")"
python3 -c 'import json, sys; body = json.load(sys.stdin); assert body["success"] is True; assert body["data"]["status"] == "UP"; assert body["data"]["schemaVersion"] >= 1' <<<"$health"

console_html="$(curl --fail --silent --show-error --insecure "$base_url/app/")"
grep -q '<div id="app"></div>' <<<"$console_html"
test "${console_html/__CSP_NONCE__/}" = "$console_html"
asset_path="$(grep -oE 'src="/app/assets/[^"]+\.js"' <<<"$console_html" | head -n 1 | cut -d '"' -f 2)"
test -n "$asset_path"
test -n "$(curl --fail --silent --show-error --insecure "$base_url$asset_path")"

cookie_jar="$temporary_directory/cookies"
login="$(curl --fail --silent --show-error --insecure \
  --cookie-jar "$cookie_jar" \
  --header 'Content-Type: application/json' \
  --data "{\"token\":\"$DBGRAPH_WEB_TOKEN\"}" \
  "$base_url/login")"
csrf_token="$(python3 -c 'import json, sys; print(json.load(sys.stdin)["data"]["csrfToken"])' <<<"$login")"
curl --fail --silent --show-error --insecure \
  --cookie "$cookie_jar" \
  --header 'Content-Type: application/json' \
  --header "X-CSRF-Token: $csrf_token" \
  --data '{"name":"container-persistence","kind":"MYSQL","dsnEnvironment":"CONTAINER_TEST_DSN","reason":"Verify container persistence"}' \
  "$base_url/api/v1/data-sources" >/dev/null

volume_name="${project_name}_dbgraph_data"
docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint /bin/sh \
  --volume "$volume_name:/data:ro" \
  "$DBGRAPH_IMAGE" -c 'test -s /data/dbgraph.sqlite'

"${compose[@]}" stop dbgraph >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container_id")" = "0"

# Simulate an interruption between replacing the generated private key and its
# matching certificate. The ownership marker means the next start may repair
# this managed pair; user-mounted certificates without the marker stay intact.
docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint /bin/sh \
  --volume "$volume_name:/data" \
  "$DBGRAPH_IMAGE" -c '
    openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
      -keyout /data/tls/key.pem.next -out /data/tls/cert.pem.next \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
    mv /data/tls/key.pem.next /data/tls/key.pem
  '
"${compose[@]}" start --wait dbgraph >/dev/null
published_address="$("${compose[@]}" port dbgraph 8443)"
base_url="https://$published_address"
curl --fail --silent --show-error --insecure "$base_url/healthz" >/dev/null
"${compose[@]}" stop dbgraph >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container_id")" = "0"

short_lived_serial="$(docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint /bin/sh \
  --volume "$volume_name:/data" \
  "$DBGRAPH_IMAGE" -c '
    openssl req -x509 -newkey rsa:2048 -sha256 -days 1 -nodes \
      -keyout /data/tls/key.pem.next -out /data/tls/cert.pem.next \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
    mv /data/tls/key.pem.next /data/tls/key.pem
    mv /data/tls/cert.pem.next /data/tls/cert.pem
    openssl x509 -noout -serial -in /data/tls/cert.pem
  ')"
"${compose[@]}" start --wait dbgraph >/dev/null

published_address="$("${compose[@]}" port dbgraph 8443)"
base_url="https://$published_address"
curl --fail --silent --show-error --insecure "$base_url/healthz" >/dev/null
renewed_serial="$(docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint openssl \
  --volume "$volume_name:/data:ro" \
  "$DBGRAPH_IMAGE" x509 -noout -serial -in /data/tls/cert.pem)"
test "$renewed_serial" != "$short_lived_serial"
docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint openssl \
  --volume "$volume_name:/data:ro" \
  "$DBGRAPH_IMAGE" x509 -checkend 2592000 -noout -in /data/tls/cert.pem >/dev/null

login="$(curl --fail --silent --show-error --insecure \
  --cookie-jar "$cookie_jar" \
  --header 'Content-Type: application/json' \
  --data "{\"token\":\"$DBGRAPH_WEB_TOKEN\"}" \
  "$base_url/login")"
python3 -c 'import json, sys; assert json.load(sys.stdin)["success"] is True' <<<"$login"
sources="$(curl --fail --silent --show-error --insecure \
  --cookie "$cookie_jar" "$base_url/api/v1/data-sources")"
python3 -c 'import json, sys; body = json.load(sys.stdin); assert any(source["name"] == "container-persistence" for source in body["data"])' <<<"$sources"

# A user-managed TLS pair has no dbgraph marker and may use any algorithm
# OpenSSL supports. It must be validated without being replaced.
"${compose[@]}" stop dbgraph >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container_id")" = "0"
custom_serial="$(docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint /bin/sh \
  --volume "$volume_name:/data" \
  "$DBGRAPH_IMAGE" -c '
    openssl ecparam -name prime256v1 -genkey -noout -out /data/tls/key.pem.next
    openssl req -new -x509 -sha256 -days 365 \
      -key /data/tls/key.pem.next -out /data/tls/cert.pem.next \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
    mv /data/tls/key.pem.next /data/tls/key.pem
    mv /data/tls/cert.pem.next /data/tls/cert.pem
    rm -f /data/tls/.generated-by-dbgraph
    openssl x509 -noout -serial -in /data/tls/cert.pem
  ')"
"${compose[@]}" start --wait dbgraph >/dev/null
published_address="$("${compose[@]}" port dbgraph 8443)"
base_url="https://$published_address"
curl --fail --silent --show-error --insecure "$base_url/healthz" >/dev/null
retained_serial="$(docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --entrypoint openssl \
  --volume "$volume_name:/data:ro" \
  "$DBGRAPH_IMAGE" x509 -noout -serial -in /data/tls/cert.pem)"
test "$retained_serial" = "$custom_serial"
