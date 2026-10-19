# The stages cloud-init runs at boot, over what the simulator rendered from
# the instance's user data under /var/lib/cloud/instances/$CI_INSTANCE_ID.
# POSIX sh: the image may carry dash, bash or BusyBox's ash.

. /var/lib/cloud/sockerless/env.sh

ci_dir=/var/lib/cloud/instances/$CI_INSTANCE_ID
ci_errors=/run/cloud-init/errors

# The distribution's cloud-init build and default user, as its cloud images
# configure them; an image whose distribution is not one of these carries no
# cloud-init.
ci_distro() {
	ci_id= ci_version_id=
	if [ -r /etc/os-release ]; then
		ci_id=$(. /etc/os-release && echo "$ID")
		ci_version_id=$(. /etc/os-release && echo "$VERSION_ID")
	fi
	case "$ci_id:$ci_version_id" in
	amzn:2023)
		CI_VERSION=22.2.2
		ci_default_name=ec2-user ci_default_gecos='EC2 Default User'
		ci_default_groups='adm wheel systemd-journal'
		;;
	ubuntu:24.04)
		CI_VERSION=24.1.3-0ubuntu3
		ci_default_name=ubuntu ci_default_gecos=Ubuntu
		ci_default_groups='adm cdrom dip lxd sudo'
		;;
	debian:12)
		# Debian's Compute Engine images run the guest environment and no
		# cloud-init.
		[ "$CI_PLATFORM" != gce ] || return 1
		CI_VERSION=22.4.2
		ci_default_gecos=Debian
		ci_default_groups='adm audio cdrom dialout dip floppy netdev plugdev sudo video'
		case "$CI_PLATFORM" in
		ec2) ci_default_name=admin ;;
		*) ci_default_name=debian ;;
		esac
		;;
	*)
		return 1
		;;
	esac
	if [ -n "$CI_DEFAULT_USER" ]; then
		ci_default_name=$CI_DEFAULT_USER
	fi
}

ci_timestamp() { date -u '+%a, %d %b %Y %H:%M:%S %z'; }

# The instance's uptime counts from its init's start, which is its boot.
ci_uptime() {
	awk -v start="$(cat /run/sockerless-boot-epoch 2>/dev/null || date +%s)" -v now="$(date +%s.%N)" \
		'BEGIN { printf "%.2f\n", now - start }'
}

ci_log() { echo "$(date -u '+%Y-%m-%d %H:%M:%S'),000 - $1"; }

ci_error() {
	ci_log "util.py[WARNING]: $1"
	mkdir -p "$(dirname "$ci_errors")"
	printf '%s\n' "$1" >>"$ci_errors"
}

ci_stage() {
	echo "Cloud-init v. $CI_VERSION running '$1' at $(ci_timestamp). Up $(ci_uptime) seconds."
}

# ci_module FREQUENCY NAME COMMAND... runs COMMAND as the module NAME, always
# or once per instance, leaving the semaphore cloud-init leaves.
ci_module() {
	freq=$1 name=$2
	shift 2
	sem=$ci_dir/sem/config_$(echo "$name" | tr - _)
	if [ "$freq" = once-per-instance ] && [ -e "$sem" ]; then
		return 0
	fi
	if ! ("$@"); then
		ci_error "Running module $name failed"
	fi
	if [ "$freq" = once-per-instance ]; then
		mkdir -p "$ci_dir/sem"
		date +%s >"$sem"
	fi
}

# ci_source FILE runs a module the simulator rendered, if the user data asked
# for it.
ci_source() {
	if [ -f "$1" ]; then
		. "$1"
	fi
}

ci_bootcmd() {
	if [ -f "$ci_dir/bootcmd" ]; then
		INSTANCE_ID=$CI_INSTANCE_ID sh "$ci_dir/bootcmd"
	fi
}

# The accounts module edits the account databases itself, as useradd and
# groupadd do, so an image without shadow-utils still gets its users.

ci_free_id() {
	awk -F: -v low="$2" -v high="$3" '
		{ used[$3] = 1 }
		END { for (i = low; i <= high; i++) if (!(i in used)) { print i; exit } }
	' "$1"
}

ci_has() { grep -q "^$2:" "$1" 2>/dev/null; }

ci_gid() { awk -F: -v g="$1" '$1 == g { print $3 }' /etc/group; }

# ci_add_group NAME [GID] creates the group unless it exists.
ci_add_group() {
	if ci_has /etc/group "$1"; then
		return 0
	fi
	gid=$2
	if [ -z "$gid" ] || awk -F: -v id="$gid" '$3 == id { found = 1 } END { exit !found }' /etc/group; then
		gid=$(ci_free_id /etc/group 1000 59999)
	fi
	echo "$1:x:$gid:" >>/etc/group
	if [ -f /etc/gshadow ]; then
		echo "$1:!::" >>/etc/gshadow
	fi
}

ci_rewrite() {
	file=$1
	shift
	awk "$@" "$file" >"$file.ci" && cat "$file.ci" >"$file" && rm -f "$file.ci"
}

# ci_group NAME [MEMBER...]
ci_group() {
	g=$1
	shift
	ci_add_group "$g"
	for member in "$@"; do
		ci_rewrite /etc/group -F: -v OFS=: -v g="$g" -v u="$member" '
			$1 == g {
				n = split($4, m, ",")
				for (i = 1; i <= n; i++) if (m[i] == u) { print; next }
				$4 = ($4 == "" ? u : $4 "," u)
			}
			{ print }
		'
	done
}

ci_home_mode() {
	mode=$(awk '$1 == "HOME_MODE" { print $2 }' /etc/login.defs 2>/dev/null)
	if [ -n "$mode" ]; then
		echo "$mode"
		return
	fi
	mask=$(awk '$1 == "UMASK" { print $2 }' /etc/login.defs 2>/dev/null)
	if [ -n "$mask" ]; then
		printf '%o\n' $((0777 & ~0$mask))
		return
	fi
	echo 700
}

# ci_user NAME GECOS HOMEDIR SHELL PRIMARY_GROUP UID SYSTEM LOCK HASHED_PASSWD GROUPS SUDO
ci_user() {
	name=$1 gecos=$2 home=$3 shell=$4 primary=$5 uid=$6 system=$7 lock=$8 hash=$9
	shift 9
	groups=$1 sudo=$2
	if ci_has /etc/passwd "$name"; then
		ci_log "cc_users_groups.py[WARNING]: User $name already exists, skipping."
	else
		if [ -z "$uid" ]; then
			if [ "$system" = 1 ]; then
				uid=$(ci_free_id /etc/passwd 100 999)
			else
				uid=$(ci_free_id /etc/passwd 1000 59999)
			fi
		fi
		[ -n "$primary" ] || primary=$name
		ci_add_group "$primary" "$uid"
		gid=$(ci_gid "$primary")
		[ -n "$home" ] || home=/home/$name
		[ -n "$shell" ] || shell=/bin/sh
		echo "$name:x:$uid:$gid:$gecos:$home:$shell" >>/etc/passwd
		if [ -f /etc/shadow ]; then
			echo "$name:!:$(($(date +%s) / 86400)):0:99999:7:::" >>/etc/shadow
		fi
		if [ "$system" != 1 ] && [ ! -e "$home" ]; then
			mkdir -p "$home"
			if [ -d /etc/skel ]; then
				cp -a /etc/skel/. "$home"/
			fi
			chown -R "$uid:$gid" "$home"
			chmod "$(ci_home_mode)" "$home"
		fi
	fi
	for g in $groups; do
		ci_group "$g" "$name"
	done
	if [ -n "$hash" ] && [ -f /etc/shadow ]; then
		ci_rewrite /etc/shadow -F: -v OFS=: -v u="$name" -v h="$hash" '$1 == u { $2 = h } { print }'
	fi
	if [ "$lock" = 1 ] && [ -f /etc/shadow ]; then
		ci_rewrite /etc/shadow -F: -v OFS=: -v u="$name" '$1 == u && substr($2, 1, 1) != "!" { $2 = "!" $2 } { print }'
	fi
	if [ -n "$sudo" ]; then
		mkdir -p /etc/sudoers.d
		f=/etc/sudoers.d/90-cloud-init-users
		if [ ! -e "$f" ]; then
			printf '# Created by cloud-init v. %s on %s\n' "$CI_VERSION" "$(ci_timestamp)" >"$f"
		fi
		printf '\n# User rules for %s\n' "$name" >>"$f"
		printf '%s\n' "$sudo" | while IFS= read -r rule; do
			printf '%s %s\n' "$name" "$rule" >>"$f"
		done
		chmod 0440 "$f"
	fi
}

ci_default_user() {
	ci_user "$ci_default_name" "$ci_default_gecos" "" /bin/bash "" "" 0 1 "" "$ci_default_groups" "ALL=(ALL) NOPASSWD:ALL"
}

# ci_keys NAME KEYFILE adds the keys in KEYFILE to the user's
# authorized_keys, keeping the ones already there.
ci_keys() {
	entry=$(grep "^$1:" /etc/passwd) || {
		ci_error "Unable to add ssh keys to user $1: the user does not exist"
		return 1
	}
	home=$(echo "$entry" | cut -d: -f6)
	owner=$(echo "$entry" | cut -d: -f3-4)
	mkdir -p "$home/.ssh"
	touch "$home/.ssh/authorized_keys"
	while IFS= read -r key; do
		[ -n "$key" ] || continue
		grep -qxF "$key" "$home/.ssh/authorized_keys" || printf '%s\n' "$key" >>"$home/.ssh/authorized_keys"
	done <"$2"
	chmod 700 "$home/.ssh"
	chmod 600 "$home/.ssh/authorized_keys"
	chown -R "$owner" "$home/.ssh"
}

# ci_root_keys USER KEYFILE gives root the keys behind cloud-init's default
# disable_root options, which turn a root login away toward USER.
ci_root_keys() {
	prefixed=$ci_dir/root-keys
	while IFS= read -r key; do
		[ -n "$key" ] || continue
		printf 'no-port-forwarding,no-agent-forwarding,no-X11-forwarding,command="echo '"'"'Please login as the user \"%s\" rather than the user \"root\".'"'"';echo;sleep 10;exit 142" %s\n' "$1" "$key"
	done <"$2" >"$prefixed"
	ci_keys root "$prefixed"
}

# ci_write_file PATH PAYLOAD OWNER PERMISSIONS APPEND
ci_write_file() {
	mkdir -p "$(dirname "$1")"
	if [ "$5" = 1 ]; then
		cat "$2" >>"$1"
	else
		cat "$2" >"$1"
	fi
	chmod "$4" "$1"
	if ! chown "$3" "$1"; then
		ci_error "Failed to set the owner of $1 to $3"
		return 1
	fi
}

# ci_packages UPDATE UPGRADE PACKAGE..., each PACKAGE a name or name=version.
ci_packages() {
	update=$1 upgrade=$2
	shift 2
	if command -v apt-get >/dev/null 2>&1; then
		export DEBIAN_FRONTEND=noninteractive
		apt() {
			apt-get --option=Dpkg::Options::=--force-confold --option=Dpkg::options::=--force-unsafe-io --assume-yes --quiet "$@"
		}
		if [ "$update" = 1 ] || [ "$upgrade" = 1 ] || [ $# -gt 0 ]; then
			apt update || return 1
		fi
		if [ "$upgrade" = 1 ]; then
			apt dist-upgrade || return 1
		fi
		if [ $# -gt 0 ]; then
			apt install "$@" || return 1
		fi
	elif command -v dnf >/dev/null 2>&1; then
		if [ "$update" = 1 ] || [ "$upgrade" = 1 ] || [ $# -gt 0 ]; then
			dnf makecache || return 1
		fi
		if [ "$upgrade" = 1 ]; then
			dnf -t -y upgrade || return 1
		fi
		if [ $# -gt 0 ]; then
			for p in "$@"; do
				shift
				set -- "$@" "$(printf '%s' "$p" | sed 's/=/-/')"
			done
			dnf install -y "$@" || return 1
		fi
	else
		echo "the image has no package manager cloud-init knows"
		return 1
	fi
}

ci_scripts_user() {
	failed=0
	for script in "$ci_dir"/scripts/*; do
		[ -f "$script" ] || continue
		chmod 700 "$script"
		"$script" || failed=1
	done
	if [ "$failed" = 1 ]; then
		ci_log "cc_scripts_user.py[WARNING]: Failed to run module scripts-user (scripts in /var/lib/cloud/instance/scripts)"
		return 1
	fi
}

ci_final() {
	up=$(ci_uptime) ts=$(ci_timestamp)
	printf '%s\n' "${CI_FINAL_MESSAGE:-Cloud-init v. \$VERSION finished at \$TIMESTAMP. Datasource \$DATASOURCE.  Up \$UPTIME seconds}" |
		sed -e "s|\\\${\\{0,1\\}VERSION}\\{0,1\\}|$CI_VERSION|g" \
			-e "s|\\\${\\{0,1\\}TIMESTAMP}\\{0,1\\}|$ts|g" \
			-e "s|\\\${\\{0,1\\}DATASOURCE}\\{0,1\\}|$CI_DATASOURCE|g" \
			-e "s|\\\${\\{0,1\\}UPTIME}\\{0,1\\}|$up|g"
	echo "$up - $ts - v. $CI_VERSION" >"$ci_dir/boot-finished"
	{
		printf '{\n "v1": {\n  "datasource": "%s",\n  "errors": [' "$CI_DATASOURCE"
		sep=
		while IFS= read -r e; do
			printf '%s\n   "%s"' "$sep" "$(printf '%s' "$e" | sed 's/\\/\\\\/g; s/"/\\"/g')"
			sep=,
		done <"$ci_errors"
		[ -z "$sep" ] || printf '\n  '
		printf ']\n }\n}\n'
	} >/run/cloud-init/result.json
	mkdir -p /var/lib/cloud/data
	cp /run/cloud-init/result.json /var/lib/cloud/data/result.json
}

ci_distro || exit 0

mkdir -p /run/cloud-init "$ci_dir/sem" /var/lib/cloud/data
: >"$ci_errors"
if [ -d /var/lib/cloud/instance ] && [ ! -L /var/lib/cloud/instance ]; then
	rm -rf /var/lib/cloud/instance
fi
ln -sfn "$ci_dir" /var/lib/cloud/instance
echo "$CI_INSTANCE_ID" >/var/lib/cloud/data/instance-id

ci_stage init
if [ -f "$ci_dir/warnings" ]; then
	while IFS= read -r w; do
		ci_log "util.py[WARNING]: $w"
	done <"$ci_dir/warnings"
fi
ci_module always bootcmd ci_bootcmd
ci_module once-per-instance write-files ci_source "$ci_dir/write-files"
ci_module once-per-instance users-groups ci_source "$ci_dir/users-groups"
ci_module once-per-instance ssh ci_source "$ci_dir/ssh"

ci_stage modules:config
ci_module once-per-instance runcmd true

ci_stage modules:final
ci_module once-per-instance package-update-upgrade-install ci_source "$ci_dir/packages"
ci_module once-per-instance write-files-deferred ci_source "$ci_dir/write-files-deferred"
ci_module once-per-instance scripts-user ci_scripts_user
ci_final
