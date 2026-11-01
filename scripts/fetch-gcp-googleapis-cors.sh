#!/usr/bin/env bash
# Captures how Google's API front end answers a browser's cross-origin calls:
# a CORS preflight and an unauthenticated GET to spanner.googleapis.com, each
# with an Origin header, into specs/cloud-api/gcp/googleapis-cors.http.gz.
#
# Google documents no CORS contract for its JSON APIs, so the front end's own
# answer is the source. The capture keeps what a client depends on: the status
# code, the CORS, Vary, Content-Type and WWW-Authenticate headers in name order,
# and the JSON body with its keys sorted, since the front end orders the
# ErrorInfo metadata differently from one answer to the next. Transport
# headers, which an HTTPS proxy on the way may add or drop, are left out. With
# OUT set it writes there and leaves SOURCES.md alone, which is how
# scripts/check-spec-freshness.sh compares a fresh capture with the vendored
# one.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
out="${OUT:-$root/specs/cloud-api/gcp/googleapis-cors.http.gz}"
url='https://spanner.googleapis.com/v1/projects/p/instances'
origin='http://localhost:4567'
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

capture() {
	curl -sS --max-time 60 -o "$work/body" -D "$work/headers" -w '%{http_code}' "$@" | sed 's/^/HTTP /'
	echo
	tr -d '\r' <"$work/headers" | grep -i -E '^(access-control-[a-z-]+|vary|content-type|www-authenticate):' |
		sed -E 's/^([^:]+):/\L\1:/' | sort -s -t: -k1,1
	if [ -s "$work/body" ]; then
		jq -S . "$work/body"
	fi
}

{
	echo "> OPTIONS $url"
	echo "> Origin: $origin"
	echo "> Access-Control-Request-Method: GET"
	echo "> Access-Control-Request-Headers: authorization,content-type"
	capture -X OPTIONS "$url" -H "Origin: $origin" \
		-H 'Access-Control-Request-Method: GET' \
		-H 'Access-Control-Request-Headers: authorization,content-type'
	echo "> GET $url"
	echo "> Origin: $origin"
	capture "$url" -H "Origin: $origin"
} | gzip -n -9 >"$out"
[ -z "${OUT:-}" ] || exit 0

fetched="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sources="$root/specs/cloud-api/gcp/SOURCES.md"
row="| \`googleapis-cors.http.gz\` | \`spanner.googleapis.com\` | \`OPTIONS and GET v1/projects/p/instances with Origin\` | — | \`captured${fetched%%T*}\` | $fetched |"
python3 - "$sources" "$row" <<'PY'
import sys
path, row = sys.argv[1], sys.argv[2]
lines = open(path, encoding='utf-8').read().splitlines()
lines = [l for l in lines if not l.startswith('| `googleapis-cors.http.gz`')]
last = max(i for i, l in enumerate(lines) if l.startswith('| `'))
lines.insert(last + 1, row)
open(path, 'w', encoding='utf-8').write('\n'.join(lines) + '\n')
PY
echo "wrote $out"
