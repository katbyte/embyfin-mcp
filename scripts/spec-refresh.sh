#!/usr/bin/env bash
# Vendor the current OpenAPI documents: pull the latest Emby and Jellyfin
# images, start each once, read the document the server serves about itself,
# fetch TMDB's, and check each in under its version - the server's own for
# Emby and Jellyfin, the date fetched (2026.09.22) for TMDB, whose document
# evolves under one API version. A document that changed is imported and generated, the
# API-level diff against the previous version is printed, and the previous
# document and definitions are removed (git holds them).
#
#   make spec-refresh                 # every service
#   scripts/spec-refresh.sh jellyfin  # one
set -euo pipefail
cd "$(dirname "$0")/.."

services=("$@")
[ ${#services[@]} -eq 0 ] && services=(emby jellyfin tmdb)
today="$(date -u +%Y.%m.%d)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# current NAME - the version, document and definitions a service is at now
current() { go run ./internal/pandorest resolve -service "$1" 2>/dev/null | cut -f2-; }

# fetch_server BACKEND PORT PATH - the document a freshly started server serves
fetch_server() {
  local backend=$1 port=$2 path=$3 env="$tmp/$1.env"
  docker pull "$(EMBYFIN_TEST_BACKEND="$backend" ./scripts/testenv.sh image)" >/dev/null
  EMBYFIN_TEST_BACKEND="$backend" EMBYFIN_TEST_CONTAINER="embyfin-mcp-spec-$backend" \
    EMBYFIN_TEST_PORT="$port" EMBYFIN_TEST_DATA="${HOME}/.cache/embyfin-mcp/testenv/spec-$backend" \
    ./scripts/testenv.sh up | grep '^export' > "$env"
  # shellcheck disable=SC1090
  . "$env"
  # Emby writes the container's host name and port into servers, which is
  # no part of the API and would make every fetch a change
  curl -sf -H "X-Emby-Token: ${EMBYFIN_TOKEN}" "${EMBYFIN_SERVER}${path}" | jq 'del(.servers)' > "$tmp/$backend.json"
  EMBYFIN_TEST_BACKEND="$backend" EMBYFIN_TEST_CONTAINER="embyfin-mcp-spec-$backend" \
    EMBYFIN_TEST_DATA="${HOME}/.cache/embyfin-mcp/testenv/spec-$backend" ./scripts/testenv.sh down >/dev/null 2>&1 || true
}

refreshed=()
for svc in "${services[@]}"; do
  case "$svc" in
    emby) fetch_server emby 18297 /emby/openapi.json; version="$(jq -r .info.version "$tmp/emby.json")" ;;
    jellyfin) fetch_server jellyfin 18296 /api-docs/openapi.json; version="$(jq -r .info.version "$tmp/jellyfin.json")" ;;
    tmdb) curl -sfL https://developer.themoviedb.org/openapi/tmdb-api.json | jq . > "$tmp/tmdb.json"; version="$today" ;;
    *) echo "unknown service $svc (emby, jellyfin, tmdb)" >&2; exit 1 ;;
  esac
  IFS=$'\t' read -r have_version have_spec have_defs <<< "$(current "$svc")"
  if [ -n "${have_spec:-}" ] && cmp -s "$tmp/$svc.json" "$have_spec"; then
    echo "$svc: $have_version is current (the document is unchanged)"
    continue
  fi
  new_spec="api-defs/${svc}-openapi-${version}.json"
  if [ "$new_spec" = "${have_spec:-}" ]; then
    echo "$svc: $version changed under the same version; replacing $have_spec in place"
  fi
  cp "$tmp/$svc.json" "$new_spec"
  echo "$svc: vendored $new_spec"
  refreshed+=("$svc	$version	$new_spec	${have_version:-}	${have_spec:-}	${have_defs:-}")
done

[ ${#refreshed[@]} -eq 0 ] && { echo "nothing to refresh"; exit 0; }

make --no-print-directory generate

for row in "${refreshed[@]}"; do
  IFS=$'\t' read -r svc version new_spec have_version have_spec have_defs <<< "$row"
  IFS=$'\t' read -r _ _ new_defs <<< "$(current "$svc")"
  if [ -n "$have_defs" ] && [ "$have_defs" != "$new_defs" ]; then
    echo
    echo "==> $svc: $have_version -> $version"
    go run ./internal/pandorest diff -old "$have_defs" -new "$new_defs" || true
    git rm -q -r "$have_defs" "$have_spec"
    echo "removed $have_spec and $have_defs (git holds them)"
  elif [ -n "$have_defs" ]; then
    echo
    echo "==> $svc: $version, changed in place; git diff shows what"
  fi
done

echo
echo "review the diffs, run make test, and commit; a workaround whose bug the new document fixes fails the import and names itself"
