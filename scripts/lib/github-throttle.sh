#!/usr/bin/env bash
# The GitHub API rate-limit protocol, kept apart from the request that obeys it
# so the decision it makes can be exercised against crafted headers — a
# throttle is not something a test can ask GitHub for.

# GH_API_MAX_THROTTLE_WAIT bounds how long one request may wait out a rate
# limit. A throttle that clears within it is waited out; one that does not is a
# credential or quota problem the run should report rather than sit on.
: "${GH_API_MAX_THROTTLE_WAIT:=120}"

# gh_throttle_wait prints how many seconds a throttled reply asked to be waited
# out, reading the headers GitHub documents for it, and fails when the reply is
# not a throttle it may wait on. The wait is never guessed: a refusal carrying
# no rate-limit signal, a spent-quota reset that never arrived, and a wait
# beyond the cap all fail rather than turning into a sleep.
#
#   $1  a file holding the reply's headers
#   $2  the current time in seconds since the Unix epoch
gh_throttle_wait() {
  local headers=$1 now=$2 wait_for reset remaining
  wait_for=$(awk 'BEGIN{IGNORECASE=1} /^retry-after:/ {gsub(/[^0-9]/,"",$2); print $2; exit}' "$headers")
  if [[ -z $wait_for ]]; then
    # Only a spent quota makes X-RateLimit-Reset meaningful; a refusal with
    # quota left is not a throttle at all.
    remaining=$(awk 'BEGIN{IGNORECASE=1} /^x-ratelimit-remaining:/ {gsub(/[^0-9]/,"",$2); print $2; exit}' "$headers")
    [[ $remaining == 0 ]] || return 1
    reset=$(awk 'BEGIN{IGNORECASE=1} /^x-ratelimit-reset:/ {gsub(/[^0-9]/,"",$2); print $2; exit}' "$headers")
    [[ -n $reset ]] || return 1
    wait_for=$((reset - now))
  fi
  # A tenth on top of the stated wait, since the two clocks are not the same
  # one, and a second so a zero-second wait still yields.
  wait_for=$((wait_for + wait_for / 10 + 1))
  ((wait_for > 0)) || wait_for=1
  ((wait_for <= GH_API_MAX_THROTTLE_WAIT)) || return 1
  printf '%s\n' "$wait_for"
}
