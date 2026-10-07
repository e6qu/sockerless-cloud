#!/bin/sh
# Build AWSAuthenticationPlugin for each engine and architecture the Amazon RDS
# data plane runs: MySQL 8.0 and MariaDB 11.4, on amd64 and arm64. clang and
# ld.lld cross-compile it with no C library.
set -eu
cd "$(dirname "$0")"
for engine in mysql mariadb; do
	define=""
	[ "$engine" = mariadb ] && define=-DMARIADB
	for target in x86_64:amd64 aarch64:arm64; do
		clang --target="${target%%:*}-linux-gnu" $define -std=c11 -O2 -Wall -Wextra -Werror \
			-fPIC -ffreestanding -fno-builtin -fvisibility=hidden -nostdlib -shared \
			-fuse-ld=lld -Wl,--build-id=none -Wl,-z,relro,-z,now -Wl,-s \
			-o "$engine-${target##*:}.so" aws_authentication_plugin.c
	done
done
