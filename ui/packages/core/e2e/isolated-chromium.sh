#!/usr/bin/env bash
# isolated-chromium.sh — run Chromium in a network namespace of its own.
#
# Chromium's Linux network-change notifier subscribes to the host's rtnetlink
# address messages and takes every one as an IP address change, answering it by
# flushing its socket pools with ERR_NETWORK_CHANGED, which aborts navigations
# and fetches still waiting for a connection. The simulators place workloads on
# the host's container engine throughout a run, and each bridge and veth pair
# the engine adds or removes is such a message. In a namespace of its own
# Chromium sees only its loopback and pasta's tap interface, which nothing
# changes; pasta forwards the named loopback ports to the host's loopback, where
# the console and its identity provider listen.
#
# Playwright launches this script as the browser executable, with:
#   SOCKERLESS_E2E_CHROMIUM    the Chromium binary to run
#   SOCKERLESS_E2E_HOST_PORTS  host loopback TCP ports to forward, comma-separated
set -euo pipefail

chromium="${SOCKERLESS_E2E_CHROMIUM:?isolated-chromium: SOCKERLESS_E2E_CHROMIUM names no Chromium binary}"
ports="${SOCKERLESS_E2E_HOST_PORTS:?isolated-chromium: SOCKERLESS_E2E_HOST_PORTS names no ports}"

if [[ ! -x "$chromium" ]]; then
  echo "isolated-chromium: no Chromium executable at $chromium; run 'bunx playwright install chromium'" >&2
  exit 1
fi
if ! command -v pasta >/dev/null; then
  echo "isolated-chromium: pasta is not installed; install the passt package" >&2
  exit 1
fi
restrict=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
if [[ "$(id -u)" -ne 0 && -r "$restrict" && "$(<"$restrict")" == 1 ]]; then
  echo "isolated-chromium: AppArmor denies capabilities in unprivileged user namespaces; run 'sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0'" >&2
  exit 1
fi

# pasta maps UID 0 into the namespace it spawns Chromium in, and the kernel
# accepts that mapping only from a holder of CAP_SETFCAP in a non-initial user
# namespace. unshare gives pasta that namespace whether the suite runs as root
# or not.
exec unshare --user --map-root-user \
  pasta --quiet --config-net \
  --tcp-ns "$ports" --udp-ns none --tcp-ports none --udp-ports none \
  -- "$chromium" "$@"
