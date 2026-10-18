#!/usr/bin/env bash
# dump-sim-diagnostics.sh — print what a simulator still running on its
# diagnostics listener is doing: the requests in flight and every goroutine.
#
# A test suite that overruns its deadline prints the test binary's stacks and
# leaves the simulator it started running; the request that never answered is
# only visible on the simulator's side. CI runs this when a suite fails, while
# that simulator still serves.
#
# Usage: scripts/dump-sim-diagnostics.sh [host:port]   (default: SIM_DIAGNOSTICS_ADDR or 127.0.0.1:6060)
set -uo pipefail

addr="${1:-${SIM_DIAGNOSTICS_ADDR:-127.0.0.1:6060}}"

# A node agent the simulator left running holds the guests' state: what it
# lists, and the taps and bridges in its namespace, say which guest kept a
# network busy.
if command -v docker >/dev/null 2>&1; then
	for agent in $(docker ps -q --filter label=sockerless-sim-node-agent=true 2>/dev/null); do
		echo "=== node agent $agent ==="
		docker logs --tail 200 "$agent" 2>&1 || true
		docker exec "$agent" sh -c 'curl -sS --max-time 5 -H "Authorization: Bearer $NODEAGENT_TOKEN" http://127.0.0.1:8700/v1/guests; echo; ip -o link; ip -o -4 addr' 2>&1 || true
	done
fi

inflight="$(curl -sS --max-time 5 "http://$addr/debug/inflight" 2>/dev/null)" || {
	echo "no simulator answers on $addr; nothing to dump"
	exit 0
}
echo "=== requests in flight on $addr ==="
printf '%s\n' "$inflight"
echo "=== goroutines on $addr (first ${SIM_GOROUTINE_LINES:-4000} lines) ==="
curl -sS --max-time 30 "http://$addr/debug/pprof/goroutine?debug=2" | head -n "${SIM_GOROUTINE_LINES:-4000}"
