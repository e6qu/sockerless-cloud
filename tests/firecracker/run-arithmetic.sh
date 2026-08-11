#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

repo_root="$(git rev-parse --show-toplevel)"
workdir="${FIRECRACKER_WORKDIR:-$repo_root/.firecracker-ci}"
version="${FIRECRACKER_VERSION:-v1.15.1}"
arch="$(uname -m)"
tap_dev="${FIRECRACKER_TAP_DEV:-fc-ci-tap0}"
tap_ip="172.16.0.1"
guest_ip="172.16.0.2"
guest_mac="06:00:AC:10:00:02"
api_socket="$workdir/firecracker.socket"
fc_pid=""
nat_iface=""
metadata_port="18080"
metadata_pid=""
metadata_dnat_installed=0

cleanup() {
  status=$?
  if [ -n "$fc_pid" ] && kill -0 "$fc_pid" >/dev/null 2>&1; then
    sudo kill "$fc_pid" >/dev/null 2>&1 || true
    wait "$fc_pid" >/dev/null 2>&1 || true
  fi
  if [ -n "$metadata_pid" ] && kill -0 "$metadata_pid" >/dev/null 2>&1; then
    kill "$metadata_pid" >/dev/null 2>&1 || true
    wait "$metadata_pid" >/dev/null 2>&1 || true
  fi
  if [ "$metadata_dnat_installed" -eq 1 ]; then
    sudo iptables -t nat -D PREROUTING -i "$tap_dev" -d 169.254.169.254 -p tcp --dport 80 -j DNAT --to-destination "$tap_ip:$metadata_port" >/dev/null 2>&1 || true
  fi
  if [ -n "$nat_iface" ]; then
    sudo iptables -t nat -D POSTROUTING -o "$nat_iface" -j MASQUERADE >/dev/null 2>&1 || true
  fi
  sudo ip link del "$tap_dev" >/dev/null 2>&1 || true
  sudo rm -f "$api_socket" >/dev/null 2>&1 || true
  if [ "$status" -ne 0 ] && [ -d "$workdir" ]; then
    for log in "$workdir/firecracker-api-response.txt" "$workdir/firecracker.log" "$workdir/firecracker-console.log"; do
      if sudo test -s "$log"; then
        echo "----- $log -----" >&2
        sudo tail -200 "$log" >&2 || true
      fi
    done
    if [ -s "$workdir/metadata-server.log" ]; then
      echo "----- $workdir/metadata-server.log -----" >&2
      tail -200 "$workdir/metadata-server.log" >&2 || true
    fi
    echo "Firecracker workdir retained for inspection: $workdir" >&2
  elif [ -d "$workdir" ]; then
    sudo rm -rf "$workdir"
  fi
}
trap cleanup EXIT

[ "$(uname -s)" = "Linux" ] || fail "Firecracker CI test requires Linux"
[ -e /dev/kvm ] || fail "Firecracker CI test requires /dev/kvm"
if ! { [ -r /dev/kvm ] && [ -w /dev/kvm ]; }; then
  sudo test -r /dev/kvm || fail "Firecracker CI test requires read access to /dev/kvm"
  sudo test -w /dev/kvm || fail "Firecracker CI test requires write access to /dev/kvm"
fi

need_cmd curl
need_cmd firecracker
need_cmd go
need_cmd ip
need_cmd iptables
need_cmd mkfs.ext4
need_cmd ssh
need_cmd ssh-keygen
need_cmd sudo
need_cmd tar
need_cmd unsquashfs

case "$arch" in
  x86_64|aarch64) ;;
  *) fail "Firecracker CI test supports x86_64 and aarch64 Linux runners; got $arch" ;;
esac

rm -rf "$workdir"
mkdir -p "$workdir"

ci_version="${version%.*}"
asset_index="$workdir/firecracker-ci-assets.xml"
curl -fsSL --retry 5 --retry-delay 2 --retry-connrefused -o "$asset_index" "https://s3.amazonaws.com/spec.ccfc.min/?prefix=firecracker-ci/${ci_version}/${arch}/&list-type=2"

kernel_key="$(tr '<' '\n' < "$asset_index" | sed -n 's#^Key>\(.*\)#\1#p' | grep -E "^firecracker-ci/${ci_version}/${arch}/vmlinux-[0-9]+\\.[0-9]+\\.[0-9]+$" | sort -V | tail -1)"
rootfs_key="$(tr '<' '\n' < "$asset_index" | sed -n 's#^Key>\(.*\)#\1#p' | grep "^firecracker-ci/${ci_version}/${arch}/ubuntu-.*[.]squashfs$" | sort -V | tail -1)"

[ -n "$kernel_key" ] || fail "could not find Firecracker CI kernel asset for $ci_version/$arch"
[ -n "$rootfs_key" ] || fail "could not find Firecracker CI Ubuntu rootfs asset for $ci_version/$arch"

kernel="$workdir/$(basename "$kernel_key")"
rootfs_squash="$workdir/$(basename "$rootfs_key")"
rootfs_dir="$workdir/rootfs"
rootfs_ext4="$workdir/rootfs.ext4"
ebs_placeholder="$workdir/ebs-placeholder.raw"
ebs_volume="$workdir/ebs-volume.raw"
ebs_snapshot="$workdir/ebs-snapshot.raw"
ebs_restored="$workdir/ebs-restored.raw"

curl -fsSL --retry 5 --retry-delay 2 --retry-connrefused -o "$kernel" "https://s3.amazonaws.com/spec.ccfc.min/${kernel_key}"
curl -fsSL --retry 5 --retry-delay 2 --retry-connrefused -o "$rootfs_squash" "https://s3.amazonaws.com/spec.ccfc.min/${rootfs_key}"

unsquashfs -quiet -d "$rootfs_dir" "$rootfs_squash"

ssh_key="$workdir/id_ed25519"
ssh-keygen -q -t ed25519 -N "" -f "$ssh_key"
mkdir -p "$rootfs_dir/root/.ssh"
cp "$ssh_key.pub" "$rootfs_dir/root/.ssh/authorized_keys"
chmod 700 "$rootfs_dir/root/.ssh"
chmod 600 "$rootfs_dir/root/.ssh/authorized_keys"

goroot="$(go env GOROOT)"
[ -d "$goroot" ] || fail "go env GOROOT did not resolve to a directory"
mkdir -p "$rootfs_dir/usr/local"
cp -a "$goroot" "$rootfs_dir/usr/local/go"

mkdir -p "$rootfs_dir/root/sockerless/testdata"
cp -R "$repo_root/testdata/eval-arithmetic" "$rootfs_dir/root/sockerless/testdata/eval-arithmetic"

cat > "$rootfs_dir/root/run-firecracker-arithmetic.sh" <<'GUESTSCRIPT'
#!/bin/sh
set -eu
export PATH=/usr/local/go/bin:/usr/sbin:/usr/bin:/sbin:/bin
export HOME=/root
export GOCACHE=/root/.cache/go-build
cd /root/sockerless/testdata/eval-arithmetic
go version
go test -count=1 ./...
go build -v -o /root/eval-arithmetic .
check() {
  expr="$1"
  want="$2"
  got="$(/root/eval-arithmetic "$expr")"
  if [ "$got" != "$want" ]; then
    echo "arithmetic mismatch for $expr: expected $want got $got" >&2
    exit 1
  fi
}
check '3 + 4 * 2' '11'
check '(10 - 3) * 2' '14'
check '100 / 5 + 1' '21'
check '2 * (3 + 4) - 1' '13'
check '1.5 + 2.5 * 2' '6.5'
cat >/root/metadata-probe.go <<'EOF'
package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://169.254.169.254/metadata-probe")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "FIRECRACKER_METADATA_OK" {
		os.Stderr.WriteString("unexpected metadata response: " + resp.Status + " " + string(body))
		os.Exit(1)
	}
}
EOF
go run /root/metadata-probe.go
echo FIRECRACKER_ARITHMETIC_OK
GUESTSCRIPT
chmod 755 "$rootfs_dir/root/run-firecracker-arithmetic.sh"

cat > "$rootfs_dir/root/configure-firecracker-network.sh" <<GUESTNETWORK
#!/bin/sh
set -eu
ip route replace default via "$tap_ip" dev eth0
echo nameserver 1.1.1.1 >/etc/resolv.conf
GUESTNETWORK
chmod 755 "$rootfs_dir/root/configure-firecracker-network.sh"

sudo chown -R root:root "$rootfs_dir"
truncate -s 3G "$rootfs_ext4"
sudo mkfs.ext4 -q -d "$rootfs_dir" -F "$rootfs_ext4"

sudo rm -f "$api_socket"
sudo ip link del "$tap_dev" >/dev/null 2>&1 || true
sudo ip tuntap add dev "$tap_dev" mode tap
sudo ip addr add "${tap_ip}/30" dev "$tap_dev"
sudo ip link set dev "$tap_dev" up
sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null

nat_iface="$(ip route show default | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
[ -n "$nat_iface" ] || fail "could not determine default network interface for guest NAT"
sudo iptables -t nat -A POSTROUTING -o "$nat_iface" -j MASQUERADE

cat > "$workdir/metadata-server.go" <<'EOF'
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: metadata-server <addr>")
	}
	http.HandleFunc("/metadata-probe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "FIRECRACKER_METADATA_OK")
	})
	log.Fatal(http.ListenAndServe(os.Args[1], nil))
}
EOF
go build -o "$workdir/metadata-server" "$workdir/metadata-server.go"
"$workdir/metadata-server" "$tap_ip:$metadata_port" > "$workdir/metadata-server.log" 2>&1 &
metadata_pid=$!
deadline=$((SECONDS + 10))
until curl -fsS "http://$tap_ip:$metadata_port/metadata-probe" >/dev/null 2>&1; do
  kill -0 "$metadata_pid" >/dev/null 2>&1 || fail "metadata probe server exited before becoming reachable"
  [ "$SECONDS" -lt "$deadline" ] || fail "timed out waiting for metadata probe server"
  sleep 0.1
done
sudo iptables -t nat -A PREROUTING -i "$tap_dev" -d 169.254.169.254 -p tcp --dport 80 -j DNAT --to-destination "$tap_ip:$metadata_port"
metadata_dnat_installed=1

run_firecracker() {
  sudo firecracker --api-sock "$api_socket" --enable-pci
}
run_firecracker > "$workdir/firecracker-console.log" 2>&1 &
fc_pid=$!

deadline=$((SECONDS + 10))
while [ ! -S "$api_socket" ]; do
  kill -0 "$fc_pid" >/dev/null 2>&1 || fail "Firecracker exited before API socket was created"
  [ "$SECONDS" -lt "$deadline" ] || fail "timed out waiting for Firecracker API socket"
  sleep 0.1
done

fc_put() {
  path="$1"
  payload="$2"
  response="$workdir/firecracker-api-response.txt"
  http_status="$(sudo curl -sS -X PUT --unix-socket "$api_socket" --data "$payload" -o "$response" -w "%{http_code}" "http://localhost${path}")" || {
    echo "Firecracker API PUT $path failed before receiving an HTTP response" >&2
    if sudo test -s "$response"; then
      sudo cat "$response" >&2
    fi
    return 1
  }
  case "$http_status" in
    2*) ;;
    *)
      echo "Firecracker API PUT $path returned HTTP $http_status" >&2
      if sudo test -s "$response"; then
        sudo cat "$response" >&2
      fi
      return 1
      ;;
  esac
  if sudo test -s "$response"; then
    :
  fi
}

fc_patch() {
  path="$1"
  payload="$2"
  response="$workdir/firecracker-api-response.txt"
  http_status="$(sudo curl -sS -X PATCH --unix-socket "$api_socket" --data "$payload" -o "$response" -w "%{http_code}" "http://localhost${path}")" || {
    echo "Firecracker API PATCH $path failed before receiving an HTTP response" >&2
    if sudo test -s "$response"; then
      sudo cat "$response" >&2
    fi
    return 1
  }
  case "$http_status" in
    2*) ;;
    *)
      echo "Firecracker API PATCH $path returned HTTP $http_status" >&2
      if sudo test -s "$response"; then
        sudo cat "$response" >&2
      fi
      return 1
      ;;
  esac
}

fc_put /logger "{
  \"log_path\": \"$workdir/firecracker.log\",
  \"level\": \"Info\",
  \"show_level\": true,
  \"show_log_origin\": true
}"

fc_put /machine-config '{
  "vcpu_count": 2,
  "mem_size_mib": 1024
}'

boot_args="console=ttyS0 reboot=k panic=1"
if [ "$arch" = "aarch64" ]; then
  boot_args="keep_bootcon $boot_args"
fi

fc_put /boot-source "{
  \"kernel_image_path\": \"$kernel\",
  \"boot_args\": \"$boot_args\"
}"

fc_put /drives/rootfs "{
  \"drive_id\": \"rootfs\",
  \"path_on_host\": \"$rootfs_ext4\",
  \"is_root_device\": true,
  \"is_read_only\": false
}"

touch "$ebs_placeholder"
fc_put /drives/ebs1 "{
  \"drive_id\": \"ebs1\",
  \"path_on_host\": \"$ebs_placeholder\",
  \"is_root_device\": false,
  \"is_read_only\": false
}"

fc_put /network-interfaces/net1 "{
  \"iface_id\": \"net1\",
  \"guest_mac\": \"$guest_mac\",
  \"host_dev_name\": \"$tap_dev\"
}"

fc_put /actions '{
  "action_type": "InstanceStart"
}'

ssh_opts=(
  -i "$ssh_key"
  -o BatchMode=yes
  -o ConnectTimeout=2
  -o LogLevel=ERROR
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
)

deadline=$((SECONDS + 120))
until ssh "${ssh_opts[@]}" "root@$guest_ip" true >/dev/null 2>&1; do
  kill -0 "$fc_pid" >/dev/null 2>&1 || fail "Firecracker exited before guest SSH became reachable"
  [ "$SECONDS" -lt "$deadline" ] || fail "timed out waiting for guest SSH at $guest_ip"
  sleep 1
done

ssh "${ssh_opts[@]}" "root@$guest_ip" /root/configure-firecracker-network.sh
truncate -s 64M "$ebs_volume"
fc_patch /drives/ebs1 "{
  \"drive_id\": \"ebs1\",
  \"path_on_host\": \"$ebs_volume\"
}"
ssh "${ssh_opts[@]}" "root@$guest_ip" '
set -eu
find_data_dev() {
  remaining=20
  while [ "$remaining" -gt 0 ]; do
    for sysdev in /sys/block/vd* /sys/block/nvme*n1; do
      [ -e "$sysdev" ] || continue
      name="${sysdev##*/}"
      [ "$name" = "vda" ] && continue
      sectors="$(cat "$sysdev/size")"
      if [ "$sectors" -gt 0 ]; then
        printf "/dev/%s\n" "$name"
        return 0
      fi
    done
    sleep 1
    remaining=$((remaining - 1))
  done
  return 1
}
dev="$(find_data_dev)"
mkfs.ext4 -q -F "$dev"
mkdir -p /mnt/sockerless-ebs
mount "$dev" /mnt/sockerless-ebs
printf "%s\n" "FIRECRACKER_EBS_PERSISTED" >/mnt/sockerless-ebs/payload.txt
sync
umount /mnt/sockerless-ebs
'
cp "$ebs_volume" "$ebs_snapshot"
cp "$ebs_snapshot" "$ebs_restored"
fc_patch /drives/ebs1 "{
  \"drive_id\": \"ebs1\",
  \"path_on_host\": \"$ebs_restored\"
}"
ssh "${ssh_opts[@]}" "root@$guest_ip" '
set -eu
find_data_dev() {
  remaining=20
  while [ "$remaining" -gt 0 ]; do
    for sysdev in /sys/block/vd* /sys/block/nvme*n1; do
      [ -e "$sysdev" ] || continue
      name="${sysdev##*/}"
      [ "$name" = "vda" ] && continue
      sectors="$(cat "$sysdev/size")"
      if [ "$sectors" -gt 0 ]; then
        printf "/dev/%s\n" "$name"
        return 0
      fi
    done
    sleep 1
    remaining=$((remaining - 1))
  done
  return 1
}
dev="$(find_data_dev)"
mkdir -p /mnt/sockerless-ebs
mount "$dev" /mnt/sockerless-ebs
grep -q FIRECRACKER_EBS_PERSISTED /mnt/sockerless-ebs/payload.txt
umount /mnt/sockerless-ebs
echo FIRECRACKER_EBS_PERSISTENCE_OK
'
ssh "${ssh_opts[@]}" "root@$guest_ip" /root/run-firecracker-arithmetic.sh | tee "$workdir/guest-arithmetic.log"
grep -q FIRECRACKER_ARITHMETIC_OK "$workdir/guest-arithmetic.log" || fail "guest arithmetic smoke did not print success marker"

ssh "${ssh_opts[@]}" "root@$guest_ip" reboot >/dev/null 2>&1 || true
deadline=$((SECONDS + 20))
while kill -0 "$fc_pid" >/dev/null 2>&1; do
  [ "$SECONDS" -lt "$deadline" ] || fail "Firecracker did not exit after guest reboot"
  sleep 1
done
