package realexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultFirecrackerVersion = "v1.15.1"

var firecrackerAssetsMu sync.Mutex

type FirecrackerVMConfig struct {
	ID            string
	Tap           *TapNIC
	MAC           string
	VCPUCount     int
	MemoryMiB     int
	WorkDir       string
	BootPeriod    time.Duration
	MetadataHosts []string
	BlockDrives   []FirecrackerBlockDrive
}

type FirecrackerBlockDrive struct {
	ID       string
	Path     string
	ReadOnly bool
}

type FirecrackerVM struct {
	ID        string
	WorkDir   string
	APISocket string
	PrivateIP net.IP

	cmd     *exec.Cmd
	cleanup *CleanupStack

	// namespace and accessKeyPath are what running a command in this machine
	// needs: the network namespace its address is reachable from, and the
	// private key whose public half was authorized in its root filesystem.
	namespace     string
	accessKeyPath string
}

type firecrackerAssets struct {
	KernelPath string
	RootFSDir  string
}

type s3ListBucketResult struct {
	Contents []s3Object `xml:"Contents"`
}

type s3Object struct {
	Key string `xml:"Key"`
}

func StartFirecrackerVM(ctx context.Context, cfg FirecrackerVMConfig) (*FirecrackerVM, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("firecracker VM ID is required")
	}
	if cfg.Tap == nil {
		return nil, fmt.Errorf("firecracker VM %s requires a TAP NIC", cfg.ID)
	}
	if cfg.MAC == "" {
		return nil, fmt.Errorf("firecracker VM %s requires a guest MAC address", cfg.ID)
	}
	if err := DetectFirecrackerCapabilities().Require(); err != nil {
		return nil, err
	}
	if cfg.VCPUCount == 0 {
		cfg.VCPUCount = 1
	}
	if cfg.MemoryMiB == 0 {
		cfg.MemoryMiB = 512
	}
	if cfg.BootPeriod == 0 {
		cfg.BootPeriod = 2 * time.Minute
	}
	workDirReady := false
	if cfg.WorkDir == "" {
		baseDir := filepath.Join(os.TempDir(), "sockerless-firecracker")
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return nil, err
		}
		sweepStaleFirecrackerWorkspaces(baseDir)
		workDir, err := os.MkdirTemp(baseDir, shortPathName(cfg.ID)+"-")
		if err != nil {
			return nil, err
		}
		cfg.WorkDir = workDir
		workDirReady = true
	}

	assets, err := ensureFirecrackerAssets(ctx, defaultFirecrackerVersion)
	if err != nil {
		return nil, err
	}

	if !workDirReady {
		_ = os.RemoveAll(cfg.WorkDir)
		if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
			return nil, err
		}
	}
	rollback := &CleanupStack{}
	rollback.Add(func(context.Context) error {
		return os.RemoveAll(cfg.WorkDir)
	})

	rootfsDir := filepath.Join(cfg.WorkDir, "rootfs")
	if err := copyRootFS(ctx, assets.RootFSDir, rootfsDir); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := ensureRootFSInit(rootfsDir); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := configureRootFSNetwork(rootfsDir, cfg.Tap.PrivateIP, cfg.Tap.Gateway, cfg.Tap.PrefixBits, cfg.MetadataHosts); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	// Authorize the host to run commands in this machine before its root
	// filesystem is packed. It is done here because once the filesystem becomes
	// an image there is no way in without one.
	accessKeyPath, err := authorizeGuestAccess(ctx, rootfsDir, cfg.WorkDir)
	if err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	rootfsPath := filepath.Join(cfg.WorkDir, "rootfs.ext4")
	if err := createExt4RootFS(ctx, rootfsDir, rootfsPath); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	// The staging tree exists to be turned into the image above. Keeping it
	// afterwards holds a second copy of the whole root filesystem for as long
	// as the machine runs, which is what fills the volume when many machines
	// are started.
	if err := os.RemoveAll(rootfsDir); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	apiSocket := filepath.Join(cfg.WorkDir, "firecracker.socket")
	_ = os.Remove(apiSocket)

	consoleLog, err := os.Create(filepath.Join(cfg.WorkDir, "firecracker-console.log"))
	if err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	defer consoleLog.Close()
	args := []string{"netns", "exec", cfg.Tap.NetworkNamespace(), "firecracker", "--api-sock", apiSocket, "--enable-pci"}
	cmd := exec.Command("ip", args...)
	cmd.Stdout = consoleLog
	cmd.Stderr = consoleLog
	if err := cmd.Start(); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	rollback.Add(func(context.Context) error {
		if cmd.Process == nil {
			return nil
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil
	})
	// Records which process the workspace belongs to, so a later machine can
	// tell an abandoned workspace from one still in use. A machine whose owner
	// is killed — a cancelled run, an expired deadline — never reaches Stop,
	// and without this its workspace stays for the life of the host.
	if cmd.Process != nil {
		_ = os.WriteFile(filepath.Join(cfg.WorkDir, firecrackerOwnerFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	}

	if err := waitForUnixSocket(ctx, apiSocket, cmd, 10*time.Second); err != nil {
		err = withFirecrackerFailureLogs(err, cfg.WorkDir)
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := configureFirecracker(ctx, apiSocket, cfg, assets.KernelPath, rootfsPath); err != nil {
		err = withFirecrackerFailureLogs(err, cfg.WorkDir)
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := waitForGuestPing(ctx, cfg.Tap.NetworkNamespace(), cfg.Tap.PrivateIP, cmd, cfg.BootPeriod); err != nil {
		err = withFirecrackerFailureLogs(err, cfg.WorkDir)
		_ = rollback.Close(context.Background())
		return nil, err
	}

	vm := &FirecrackerVM{
		ID:            cfg.ID,
		WorkDir:       cfg.WorkDir,
		APISocket:     apiSocket,
		PrivateIP:     append(net.IP(nil), cfg.Tap.PrivateIP...),
		cmd:           cmd,
		cleanup:       rollback,
		namespace:     cfg.Tap.NetworkNamespace(),
		accessKeyPath: accessKeyPath,
	}
	return vm, nil
}

func DetectFirecrackerCapabilities() CapabilityReport {
	return DetectCapabilities(firecrackerRequiredCommands()...)
}

func firecrackerRequiredCommands() []string {
	return []string{"cp", "du", "firecracker", "ip", "nft", "mkfs.ext4", "ping", "unsquashfs"}
}

func (vm *FirecrackerVM) Stop(ctx context.Context) error {
	if vm == nil || vm.cleanup == nil {
		return nil
	}
	return vm.cleanup.Close(ctx)
}

func (vm *FirecrackerVM) Alive() bool {
	if vm == nil || vm.cmd == nil || vm.cmd.Process == nil {
		return false
	}
	return vm.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func (vm *FirecrackerVM) PatchBlockDrivePath(ctx context.Context, driveID, path string) error {
	if vm == nil {
		return fmt.Errorf("firecracker VM is nil")
	}
	if driveID == "" {
		return fmt.Errorf("firecracker drive ID is required")
	}
	if path == "" {
		return fmt.Errorf("firecracker drive %s requires a backing path", driveID)
	}
	body, err := json.Marshal(struct {
		DriveID    string `json:"drive_id"`
		PathOnHost string `json:"path_on_host"`
	}{
		DriveID:    driveID,
		PathOnHost: path,
	})
	if err != nil {
		return err
	}
	return firecrackerPatch(ctx, vm.APISocket, "/drives/"+driveID, string(body))
}

func ensureFirecrackerAssets(ctx context.Context, version string) (firecrackerAssets, error) {
	firecrackerAssetsMu.Lock()
	defer firecrackerAssetsMu.Unlock()

	arch, err := firecrackerAssetArch()
	if err != nil {
		return firecrackerAssets{}, err
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return firecrackerAssets{}, err
	}
	ciVersion := strings.TrimSuffix(version, filepath.Ext(version))
	dir := filepath.Join(cacheRoot, "sockerless", "firecracker-ci", ciVersion, arch)
	kernelMarker := filepath.Join(dir, "kernel.path")
	rootfsMarker := filepath.Join(dir, "rootfs.ready")
	if kernel, err := os.ReadFile(kernelMarker); err == nil {
		rootfsDir := filepath.Join(dir, "rootfs")
		kernelPath := strings.TrimSpace(string(kernel))
		// A cached file that is not a kernel is evicted whether or not the root
		// filesystem finished unpacking. Evicting it only when both markers
		// were present left a truncated download in place after an interrupted
		// fetch, and downloadFile treats an existing path as already
		// downloaded — so one interrupted fetch poisoned the cache for every
		// later run with "is not an ELF image", recoverable only by deleting
		// the cache by hand.
		if err := verifyELFKernel(kernelPath); err != nil {
			_ = os.Remove(kernelMarker)
			_ = os.Remove(kernelPath)
		} else if _, statErr := os.Stat(rootfsMarker); statErr == nil {
			return firecrackerAssets{KernelPath: kernelPath, RootFSDir: rootfsDir}, nil
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return firecrackerAssets{}, err
	}
	kernelKey, rootfsKey, err := findFirecrackerAssetKeys(ctx, ciVersion, arch)
	if err != nil {
		return firecrackerAssets{}, err
	}
	kernelPath := filepath.Join(dir, filepath.Base(kernelKey))
	rootfsSquash := filepath.Join(dir, filepath.Base(rootfsKey))
	// The same eviction for a kernel left behind without a marker to find it
	// by: downloadFile would otherwise skip the fetch and hand back the
	// unusable file.
	if _, err := os.Stat(kernelPath); err == nil {
		if err := verifyELFKernel(kernelPath); err != nil {
			_ = os.Remove(kernelPath)
		}
	}
	if err := downloadFile(ctx, "https://s3.amazonaws.com/spec.ccfc.min/"+kernelKey, kernelPath); err != nil {
		return firecrackerAssets{}, err
	}
	if err := verifyELFKernel(kernelPath); err != nil {
		_ = os.Remove(kernelPath)
		return firecrackerAssets{}, err
	}
	if err := downloadFile(ctx, "https://s3.amazonaws.com/spec.ccfc.min/"+rootfsKey, rootfsSquash); err != nil {
		return firecrackerAssets{}, err
	}
	rootfsDir := filepath.Join(dir, "rootfs")
	_ = os.RemoveAll(rootfsDir)
	if err := (Runner{}).Run(ctx, "unsquashfs", "-quiet", "-d", rootfsDir, rootfsSquash); err != nil {
		return firecrackerAssets{}, err
	}
	if err := os.WriteFile(kernelMarker, []byte(kernelPath+"\n"), 0o644); err != nil {
		return firecrackerAssets{}, err
	}
	if err := os.WriteFile(rootfsMarker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return firecrackerAssets{}, err
	}
	return firecrackerAssets{KernelPath: kernelPath, RootFSDir: rootfsDir}, nil
}

func firecrackerAssetArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("firecracker CI assets support amd64 and arm64 hosts; got %s", runtime.GOARCH)
	}
}

func findFirecrackerAssetKeys(ctx context.Context, ciVersion, arch string) (string, string, error) {
	url := fmt.Sprintf("https://s3.amazonaws.com/spec.ccfc.min/?prefix=firecracker-ci/%s/%s/&list-type=2", ciVersion, arch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("list Firecracker CI assets: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var listing s3ListBucketResult
	if err := xml.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return "", "", err
	}
	var kernels, rootfs []string
	prefix := fmt.Sprintf("firecracker-ci/%s/%s/", ciVersion, arch)
	for _, obj := range listing.Contents {
		if isFirecrackerKernelAsset(prefix, obj.Key) {
			kernels = append(kernels, obj.Key)
		}
		if strings.HasPrefix(obj.Key, prefix+"ubuntu-") && strings.HasSuffix(obj.Key, ".squashfs") {
			rootfs = append(rootfs, obj.Key)
		}
	}
	sort.Slice(kernels, func(i, j int) bool { return firecrackerAssetKeyLess(kernels[i], kernels[j]) })
	sort.Slice(rootfs, func(i, j int) bool { return firecrackerAssetKeyLess(rootfs[i], rootfs[j]) })
	if len(kernels) == 0 {
		return "", "", fmt.Errorf("no Firecracker CI kernel asset found for %s/%s", ciVersion, arch)
	}
	if len(rootfs) == 0 {
		return "", "", fmt.Errorf("no Firecracker CI Ubuntu rootfs asset found for %s/%s", ciVersion, arch)
	}
	return kernels[len(kernels)-1], rootfs[len(rootfs)-1], nil
}

func isFirecrackerKernelAsset(prefix, key string) bool {
	if !strings.HasPrefix(key, prefix+"vmlinux-") {
		return false
	}
	base := filepath.Base(key)
	return !strings.HasSuffix(base, ".config") && !strings.HasSuffix(base, ".debug.gz")
}

func firecrackerAssetKeyLess(a, b string) bool {
	abase := filepath.Base(a)
	bbase := filepath.Base(b)
	av := versionParts(abase)
	bv := versionParts(bbase)
	for i := 0; i < len(av) || i < len(bv); i++ {
		var ai, bi int
		if i < len(av) {
			ai = av[i]
		}
		if i < len(bv) {
			bi = bv[i]
		}
		if ai != bi {
			return ai < bi
		}
	}
	return abase < bbase
}

func versionParts(value string) []int {
	var parts []int
	var digits strings.Builder
	flush := func() {
		if digits.Len() == 0 {
			return
		}
		n, err := strconv.Atoi(digits.String())
		if err == nil {
			parts = append(parts, n)
		}
		digits.Reset()
	}
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return parts
}

func downloadFile(ctx context.Context, url, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// verifyELFKernel checks that a downloaded file is the kernel image format
// Firecracker boots on this architecture. The format is architecture-specific
// and not a matter of preference: on x86_64 Firecracker takes an uncompressed
// ELF vmlinux, while on aarch64 it takes the arm64 Linux `Image`, which is a
// PE/COFF file beginning "MZ". Demanding ELF everywhere rejected the aarch64
// asset that Firecracker's own CI publishes — a valid kernel reported as "not
// an ELF image", which reads like a corrupted download and sends the reader
// looking for a network fault that is not there.
func verifyELFKernel(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("read Firecracker kernel magic %s: %w", path, err)
	}
	switch runtime.GOARCH {
	case "arm64":
		if magic[0] != 'M' || magic[1] != 'Z' {
			return fmt.Errorf("firecracker kernel %s is not an arm64 PE image", path)
		}
	default:
		if magic != [4]byte{0x7f, 'E', 'L', 'F'} {
			return fmt.Errorf("firecracker kernel %s is not an ELF image", path)
		}
	}
	return nil
}

func copyRootFS(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return (Runner{}).Run(ctx, "cp", "-a", src+string(os.PathSeparator)+".", dst)
}

func ensureRootFSInit(rootfsDir string) error {
	for _, rel := range []string{"sbin/init", "etc/init", "bin/init", "bin/sh"} {
		if _, err := os.Stat(filepath.Join(rootfsDir, filepath.FromSlash(rel))); err == nil {
			return nil
		}
	}

	const systemd = "/usr/lib/systemd/systemd"
	systemdPath := filepath.Join(rootfsDir, filepath.FromSlash(strings.TrimPrefix(systemd, "/")))
	if info, err := os.Stat(systemdPath); err == nil && !info.IsDir() {
		sbinDir := filepath.Join(rootfsDir, "sbin")
		if err := os.MkdirAll(sbinDir, 0o755); err != nil {
			return err
		}
		initPath := filepath.Join(sbinDir, "init")
		if err := os.Remove(initPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(systemd, initPath); err != nil {
			return fmt.Errorf("link rootfs /sbin/init to systemd: %w", err)
		}
		return nil
	}

	return fmt.Errorf("rootfs has no kernel init candidate (/sbin/init, /etc/init, /bin/init, /bin/sh) and no %s", systemd)
}

func configureRootFSNetwork(rootfsDir string, ip, gateway net.IP, prefixBits int, metadataHosts []string) error {
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("guest IPv4 address is required")
	}
	if gateway == nil || gateway.To4() == nil {
		return fmt.Errorf("guest IPv4 gateway is required")
	}
	if err := disableStockFirecrackerNetworking(rootfsDir); err != nil {
		return err
	}
	systemdDir := filepath.Join(rootfsDir, "etc", "systemd", "network")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return err
	}
	if entries, err := os.ReadDir(systemdDir); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".network") {
				if err := os.Remove(filepath.Join(systemdDir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	networkd := fmt.Sprintf(`[Match]
Name=eth0

[Network]
Address=%s/%d
Gateway=%s
DNS=1.1.1.1
`, ip, prefixBits, gateway)
	if err := os.WriteFile(filepath.Join(systemdDir, "10-sockerless-eth0.network"), []byte(networkd), 0o644); err != nil {
		return err
	}
	netplanDir := filepath.Join(rootfsDir, "etc", "netplan")
	if err := os.MkdirAll(netplanDir, 0o755); err != nil {
		return err
	}
	if entries, err := os.ReadDir(netplanDir); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml") {
				if err := os.Remove(filepath.Join(netplanDir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	netplan := fmt.Sprintf(`network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      addresses:
        - %s/%d
      routes:
        - to: default
          via: %s
      nameservers:
        addresses:
          - 1.1.1.1
`, ip, prefixBits, gateway)
	if err := os.WriteFile(filepath.Join(netplanDir, "10-sockerless.yaml"), []byte(netplan), 0o600); err != nil {
		return err
	}
	interfacesDir := filepath.Join(rootfsDir, "etc", "network", "interfaces.d")
	if err := os.MkdirAll(interfacesDir, 0o755); err != nil {
		return err
	}
	resolvDir := filepath.Join(rootfsDir, "etc")
	if err := os.MkdirAll(resolvDir, 0o755); err != nil {
		return err
	}
	ifupdown := fmt.Sprintf(`auto eth0
iface eth0 inet static
    address %s
    netmask %s
    gateway %s
    dns-nameservers 1.1.1.1
`, ip, prefixNetmask(prefixBits), gateway)
	if err := os.WriteFile(filepath.Join(rootfsDir, "etc", "network", "interfaces"), []byte(ifupdown), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(interfacesDir, "sockerless-eth0"), []byte(ifupdown), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resolvDir, "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		return err
	}
	if err := configureRootFSMetadataHosts(rootfsDir, metadataHosts); err != nil {
		return err
	}
	sbinDir := filepath.Join(rootfsDir, "usr", "local", "sbin")
	if err := os.MkdirAll(sbinDir, 0o755); err != nil {
		return err
	}
	configureScript := fmt.Sprintf(`#!/bin/sh
set -eu
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
dev="${SOCKERLESS_NET_IFACE:-eth0}"
if [ ! -d "/sys/class/net/$dev" ]; then
  dev=""
  for path in /sys/class/net/*; do
    name="${path##*/}"
    if [ "$name" != "lo" ]; then
      dev="$name"
      break
    fi
  done
fi
if [ -z "$dev" ]; then
  echo "no non-loopback network interface found" >&2
  exit 1
fi
ip link set dev "$dev" up
ip addr flush dev "$dev" scope global || true
ip addr replace %s/%d dev "$dev"
ip route replace default via %s dev "$dev"
printf 'nameserver 1.1.1.1\n' > /etc/resolv.conf
`, ip, prefixBits, gateway)
	if err := os.WriteFile(filepath.Join(sbinDir, "sockerless-network"), []byte(configureScript), 0o755); err != nil {
		return err
	}
	serviceDir := filepath.Join(rootfsDir, "etc", "systemd", "system")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return err
	}
	service := `[Unit]
Description=Sockerless cloud simulator guest networking
After=systemd-udevd.service systemd-udev-trigger.service
Before=network-online.target multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/sockerless-network
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
	if err := os.WriteFile(filepath.Join(serviceDir, "sockerless-network.service"), []byte(service), 0o644); err != nil {
		return err
	}
	wantsDir := filepath.Join(serviceDir, "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(wantsDir, "sockerless-network.service")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink("../sockerless-network.service", link)
}

func configureRootFSMetadataHosts(rootfsDir string, metadataHosts []string) error {
	if len(metadataHosts) == 0 {
		return nil
	}
	hostsPath := filepath.Join(rootfsDir, "etc", "hosts")
	existing, err := os.ReadFile(hostsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var hosts []string
	for _, host := range metadataHosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return nil
	}
	line := MetadataIPv4 + " " + strings.Join(hosts, " ")
	if strings.Contains(string(existing), line) {
		return nil
	}
	var out strings.Builder
	out.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString(line)
	out.WriteByte('\n')
	return os.WriteFile(hostsPath, []byte(out.String()), 0o644)
}

func disableStockFirecrackerNetworking(rootfsDir string) error {
	const (
		fcnetService      = "fcnet.service"
		fcnetSetupService = "fcnet-setup.service"
	)
	paths := []string{
		"etc/init.d/fcnet",
		"etc/init.d/fcnet-setup",
		"etc/network/interfaces.d/fcnet",
		"etc/network/interfaces.d/fcnet.cfg",
		"lib/systemd/system/fcnet.service",
		"lib/systemd/system/fcnet-setup.service",
		"usr/lib/systemd/system/fcnet.service",
		"usr/lib/systemd/system/fcnet-setup.service",
		"usr/local/bin/fcnet-setup.sh",
		"usr/local/sbin/fcnet-setup.sh",
		"usr/bin/fcnet-setup.sh",
		"usr/sbin/fcnet-setup.sh",
	}
	for _, rel := range paths {
		if err := removeRootFSPath(rootfsDir, rel); err != nil {
			return err
		}
	}
	patterns := []string{
		"etc/network/interfaces.d/*fcnet*",
		"etc/rc*.d/*fcnet*",
		"etc/systemd/system/*.wants/*fcnet*",
		"etc/systemd/system/*.requires/*fcnet*",
		"etc/systemd/system/*.target.wants/*fcnet*",
		"etc/systemd/system/*.target.requires/*fcnet*",
		"etc/systemd/network/*fcnet*.network",
	}
	for _, pattern := range patterns {
		if err := removeRootFSGlob(rootfsDir, pattern); err != nil {
			return err
		}
	}

	serviceDir := filepath.Join(rootfsDir, "etc", "systemd", "system")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return err
	}
	for _, service := range []string{fcnetService, fcnetSetupService} {
		mask := filepath.Join(serviceDir, service)
		if err := os.RemoveAll(mask); err != nil {
			return fmt.Errorf("remove stock Firecracker network unit %s: %w", service, err)
		}
		if err := os.Symlink("/dev/null", mask); err != nil {
			return fmt.Errorf("mask stock Firecracker network unit %s: %w", service, err)
		}
	}
	return nil
}

func removeRootFSPath(rootfsDir, rel string) error {
	path := filepath.Join(rootfsDir, filepath.FromSlash(rel))
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove stock Firecracker rootfs network path %s: %w", rel, err)
	}
	return nil
}

func removeRootFSGlob(rootfsDir, pattern string) error {
	matches, err := filepath.Glob(filepath.Join(rootfsDir, filepath.FromSlash(pattern)))
	if err != nil {
		return fmt.Errorf("match stock Firecracker rootfs network path %s: %w", pattern, err)
	}
	for _, path := range matches {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove stock Firecracker rootfs network path %s: %w", path, err)
		}
	}
	return nil
}

func createExt4RootFS(ctx context.Context, rootfsDir, rootfsPath string) error {
	size, err := ext4RootFSImageSize(ctx, rootfsDir)
	if err != nil {
		return err
	}
	rootfs, err := os.OpenFile(rootfsPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := rootfs.Truncate(int64(size)); err != nil {
		_ = rootfs.Close()
		return err
	}
	if err := rootfs.Close(); err != nil {
		return err
	}
	return (Runner{}).Run(ctx, "mkfs.ext4", "-q", "-d", rootfsDir, "-F", rootfsPath)
}

func ext4RootFSImageSize(ctx context.Context, rootfsDir string) (uint64, error) {
	out, err := (Runner{}).Output(ctx, "du", "-sk", rootfsDir)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("du -sk %s returned no size", rootfsDir)
	}
	usedKB, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse rootfs size from du output %q: %w", out, err)
	}
	return ext4RootFSImageSizeBytes(usedKB), nil
}

func ext4RootFSImageSizeBytes(usedKB uint64) uint64 {
	const (
		kib           = uint64(1024)
		mib           = uint64(1024 * 1024)
		minSize       = 1024 * mib
		extraHeadroom = 256 * mib
		alignment     = 64 * mib
	)
	size := usedKB*kib*2 + extraHeadroom
	if size < minSize {
		size = minSize
	}
	if rem := size % alignment; rem != 0 {
		size += alignment - rem
	}
	return size
}

func waitForUnixSocket(ctx context.Context, socket string, cmd *exec.Cmd, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		if cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) != nil {
			return fmt.Errorf("firecracker exited before API socket was created")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Firecracker API socket %s", socket)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func configureFirecracker(ctx context.Context, apiSocket string, cfg FirecrackerVMConfig, kernelPath, rootfsPath string) error {
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 ip=%s::%s:%s::eth0:off nameserver=1.1.1.1",
		cfg.Tap.PrivateIP, cfg.Tap.Gateway, prefixNetmask(cfg.Tap.PrefixBits))
	if runtime.GOARCH == "arm64" {
		bootArgs = "keep_bootcon " + bootArgs
	}
	requests := []struct {
		path string
		body string
	}{
		{"/logger", fmt.Sprintf(`{"log_path":%q,"level":"Info","show_level":true,"show_log_origin":true}`, filepath.Join(cfg.WorkDir, "firecracker.log"))},
		{"/machine-config", fmt.Sprintf(`{"vcpu_count":%d,"mem_size_mib":%d}`, cfg.VCPUCount, cfg.MemoryMiB)},
		{"/boot-source", fmt.Sprintf(`{"kernel_image_path":%q,"boot_args":%q}`, kernelPath, bootArgs)},
		{"/drives/rootfs", fmt.Sprintf(`{"drive_id":"rootfs","path_on_host":%q,"is_root_device":true,"is_read_only":false}`, rootfsPath)},
	}
	for _, drive := range cfg.BlockDrives {
		if drive.ID == "" {
			return fmt.Errorf("firecracker non-root block drive ID is required")
		}
		if drive.ID == "rootfs" {
			return fmt.Errorf("firecracker non-root block drive ID cannot be rootfs")
		}
		if drive.Path == "" {
			return fmt.Errorf("firecracker block drive %s requires a host path", drive.ID)
		}
		body, err := json.Marshal(struct {
			DriveID      string `json:"drive_id"`
			PathOnHost   string `json:"path_on_host"`
			IsRootDevice bool   `json:"is_root_device"`
			IsReadOnly   bool   `json:"is_read_only"`
		}{
			DriveID:      drive.ID,
			PathOnHost:   drive.Path,
			IsRootDevice: false,
			IsReadOnly:   drive.ReadOnly,
		})
		if err != nil {
			return err
		}
		requests = append(requests, struct {
			path string
			body string
		}{"/drives/" + drive.ID, string(body)})
	}
	requests = append(requests,
		struct {
			path string
			body string
		}{"/network-interfaces/net1", fmt.Sprintf(`{"iface_id":"net1","guest_mac":%q,"host_dev_name":%q}`, cfg.MAC, cfg.Tap.TapName)},
		struct {
			path string
			body string
		}{"/actions", `{"action_type":"InstanceStart"}`},
	)
	for _, req := range requests {
		if err := firecrackerPut(ctx, apiSocket, req.path, req.body); err != nil {
			return err
		}
	}
	return nil
}

func firecrackerPut(ctx context.Context, socket, path, payload string) error {
	return firecrackerRequest(ctx, http.MethodPut, socket, path, payload)
}

func firecrackerPatch(ctx context.Context, socket, path, payload string) error {
	return firecrackerRequest(ctx, http.MethodPatch, socket, path, payload)
}

func firecrackerRequest(ctx context.Context, method, socket, path, payload string) error {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, bytes.NewBufferString(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("firecracker API %s %s returned HTTP %d: %s", method, path, resp.StatusCode, string(body))
	}
	return nil
}

func waitForGuestPing(ctx context.Context, namespace string, ip net.IP, cmd *exec.Cmd, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ping := exec.CommandContext(ctx, "ip", "netns", "exec", namespace, "ping", "-c", "1", "-W", "1", ip.String())
		if err := ping.Run(); err == nil {
			return nil
		}
		if cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) != nil {
			return fmt.Errorf("firecracker exited before guest %s became reachable", ip)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Firecracker guest %s reachability", ip)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

func withFirecrackerFailureLogs(err error, workDir string) error {
	logs := firecrackerFailureLogs(workDir)
	if logs == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, logs)
}

func firecrackerFailureLogs(workDir string) string {
	var parts []string
	for _, name := range []string{"firecracker-console.log", "firecracker.log"} {
		path := filepath.Join(workDir, name)
		body, err := readTail(path, 8192)
		if err != nil || strings.TrimSpace(body) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("----- %s -----\n%s", name, body))
	}
	return strings.Join(parts, "\n")
}

func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return "", err
	}
	if offset > 0 {
		return "[truncated]\n" + string(buf), nil
	}
	return string(buf), nil
}

func prefixNetmask(prefixBits int) net.IP {
	mask := net.CIDRMask(prefixBits, 32)
	return net.IPv4(mask[0], mask[1], mask[2], mask[3])
}

func sanitizePathName(value string) string {
	replacer := strings.NewReplacer("/", "-", ":", "-", " ", "-", "\t", "-", "\n", "-")
	return replacer.Replace(value)
}

func shortPathName(value string) string {
	sanitized := sanitizePathName(value)
	if len(sanitized) <= 16 {
		return sanitized
	}
	sum := sha256.Sum256([]byte(value))
	return sanitized[:16] + "-" + hex.EncodeToString(sum[:6])
}

// firecrackerOwnerFile records the process a workspace belongs to.
const firecrackerOwnerFile = "firecracker.owner"

// firecrackerWorkspaceGrace is how long a workspace with no recorded owner is
// left alone. A workspace is created before its process starts, so a young one
// may simply not have reached that point yet.
const firecrackerWorkspaceGrace = 10 * time.Minute

// sweepStaleFirecrackerWorkspaces removes workspaces whose machine is gone.
// Each holds a root filesystem image, so on a host that starts many machines
// they accumulate until the volume is full — and a machine that is killed
// rather than stopped never removes its own.
func sweepStaleFirecrackerWorkspaces(baseDir string) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, entry.Name())
		if firecrackerWorkspaceInUse(dir) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

func firecrackerWorkspaceInUse(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, firecrackerOwnerFile))
	if err != nil {
		info, statErr := os.Stat(dir)
		return statErr != nil || time.Since(info.ModTime()) < firecrackerWorkspaceGrace
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
