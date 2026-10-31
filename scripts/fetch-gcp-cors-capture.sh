#!/usr/bin/env bash
# Captures how Google's API front end answers a browser's cross-origin calls:
# a CORS preflight and an unauthenticated GET to spanner.googleapis.com, each
# with an Origin header, into specs/cloud-api/gcp/googleapis-cors.http.gz.
#
# Google documents no CORS contract for its JSON APIs, so the front end's own
# answer is the source. The capture keeps the status lines and headers and
# drops the volatile date and server-timing values.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
out="$root/specs/cloud-api/gcp/googleapis-cors.http.gz"
url='https://spanner.googleapis.com/v1/projects/p/instances'
origin='http://localhost:4567'

capture() {
	# An HTTPS proxy's own "Connection Established" response precedes Google's.
	curl -sS --max-time 60 -o /dev/null -D - "$@" | tr -d '\r' |
		awk 'tolower($0) ~ /^http\/1\.[01] 200 connection established/ { skip = 1; next } skip && /^$/ { skip = 0; next } !skip' |
		grep -v -i -E '^(date|server-timing):'
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

fetched="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sources="$root/specs/cloud-api/gcp/SOURCES.md"
row="| \`googleapis-cors.http.gz\` | \`spanner.googleapis.com\` | \`OPTIONS and GET v1/projects/p/instances with Origin\` | — | \`live capture\` | $fetched |"
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
