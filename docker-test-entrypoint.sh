#!/usr/bin/env bash
set -euo pipefail

setup_localhost_dns() {
    if ! command -v dnsmasq >/dev/null 2>&1; then
        return
    fi

    cp /etc/resolv.conf /tmp/sockerless-upstream-resolv.conf
    cat >/tmp/sockerless-dnsmasq.conf <<'EOF'
address=/localhost/127.0.0.1
listen-address=127.0.0.1
bind-interfaces
resolv-file=/tmp/sockerless-upstream-resolv.conf
pid-file=/tmp/sockerless-dnsmasq.pid
EOF
    dnsmasq --conf-file=/tmp/sockerless-dnsmasq.conf
    printf 'nameserver 127.0.0.1\noptions ndots:0\n' >/etc/resolv.conf
}

drop_to_host_user() {
    if [ -z "${SOCKERLESS_DOCKER_TEST_UIDGID:-}" ]; then
        exec "$@"
    fi

    uid="${SOCKERLESS_DOCKER_TEST_UIDGID%%:*}"
    gid="${SOCKERLESS_DOCKER_TEST_UIDGID##*:}"
    groups="${SOCKERLESS_DOCKER_TEST_GROUPS:-}"
    if [ -n "$groups" ]; then
        groups="$gid,$groups"
    else
        groups="$gid"
    fi

    exec setpriv --reuid="$uid" --regid="$gid" --groups="$groups" "$@"
}

mkdir -p "${HOME:-/tmp/sockerless-home}"
setup_localhost_dns
drop_to_host_user "$@"
