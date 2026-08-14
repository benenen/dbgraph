#!/bin/sh

set -eu

if [ "${1:-}" = "serve" ]; then
  certificate_file="${DBGRAPH_TLS_CERT_FILE:-}"
  key_file="${DBGRAPH_TLS_KEY_FILE:-}"
  certificate_directory="$(dirname "$certificate_file")"
  generated_marker="$certificate_directory/.generated-by-dbgraph"

  generate_certificate() {
    umask 077
    mkdir -p "$certificate_directory" "$(dirname "$key_file")"
    next_certificate="$certificate_file.next"
    next_key="$key_file.next"
    : >"$generated_marker"
    openssl req -x509 -newkey rsa:3072 -sha256 -days 365 -nodes \
      -keyout "$next_key" \
      -out "$next_certificate" \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
    mv "$next_key" "$key_file"
    mv "$next_certificate" "$certificate_file"
    echo "Generated a local TLS certificate at $certificate_file" >&2
  }

  certificate_pair_is_valid() {
    [ -r "$certificate_file" ] && [ -r "$key_file" ] || return 1
    certificate_public_key="$(openssl x509 -pubkey -noout -in "$certificate_file" 2>/dev/null)" || return 1
    private_public_key="$(openssl pkey -pubout -in "$key_file" 2>/dev/null)" || return 1
    [ -n "$certificate_public_key" ] && [ "$certificate_public_key" = "$private_public_key" ]
  }

  if [ -z "$certificate_file" ] || [ -z "$key_file" ]; then
    echo "DBGRAPH_TLS_CERT_FILE and DBGRAPH_TLS_KEY_FILE are required" >&2
    exit 1
  fi

  if [ ! -e "$certificate_file" ] && [ ! -e "$key_file" ]; then
    generate_certificate
  elif [ -e "$generated_marker" ]; then
    if ! certificate_pair_is_valid \
      || ! openssl x509 -checkend 2592000 -noout -in "$certificate_file" >/dev/null 2>&1; then
      generate_certificate
    fi
  elif ! certificate_pair_is_valid; then
    echo "TLS certificate and key must both exist, be readable, and match" >&2
    exit 1
  fi
fi

exec /usr/local/bin/dbgraph "$@"
