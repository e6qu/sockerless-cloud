package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	controlPort = 5000
	exitPort    = 5001
)

// launchFirecracker starts the machine over Firecracker's API: the kernel the
// agent carries, the guest's root image, its empty disk slots, its tap and a
// vsock for guestinit, then the boot. The returned channel closes when the
// Firecracker process ends.
func (g *guest) launchFirecracker(ctx context.Context, tap string, extraTaps []string) (*exec.Cmd, <-chan struct{}, error) {
	api := filepath.Join(g.dir, "firecracker.socket")
	_ = os.Remove(api)
	_ = os.Remove(g.vsockPath())
	consolePath := filepath.Join(g.dir, "console.log")
	console, err := os.Create(consolePath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(g.agent.cfg.firecracker, "--api-sock", api)
	cmd.Stdout = console
	cmd.Stderr = console
	if err := cmd.Start(); err != nil {
		_ = console.Close()
		return nil, nil, fmt.Errorf("start firecracker: %w", err)
	}
	_ = console.Close()
	ended := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(ended)
	}()
	kill := func() {
		_ = cmd.Process.Kill()
		<-ended
	}
	if err := waitForSocket(ctx, api, 10*time.Second, ended, consolePath); err != nil {
		kill()
		return nil, nil, err
	}
	mask := net.CIDRMask(g.address.Bits(), 32)
	// An empty gateway in the kernel's ip= leaves the guest without a
	// default route, as a subnet with no way out does.
	via, err := g.spec.Network.nextHop()
	if err != nil {
		kill()
		return nil, nil, err
	}
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off init=/sbin/sockerless-init ip=%s::%s:%s::eth0:off",
		g.address.Addr(), via, net.IP(mask))
	if runtime.GOARCH == "arm64" {
		bootArgs = "keep_bootcon " + bootArgs
	}
	requests := []struct{ path, body string }{
		{"/logger", fmt.Sprintf(`{"log_path":%q,"level":"Info"}`, filepath.Join(g.dir, "firecracker.log"))},
		{"/machine-config", fmt.Sprintf(`{"vcpu_count":%d,"mem_size_mib":%d}`, g.spec.VCPUs, g.spec.MemoryMiB)},
		{"/boot-source", fmt.Sprintf(`{"kernel_image_path":%q,"boot_args":%q}`, g.agent.cfg.kernel, bootArgs)},
		{"/drives/rootfs", fmt.Sprintf(`{"drive_id":"rootfs","path_on_host":%q,"is_root_device":true,"is_read_only":false}`, filepath.Join(g.dir, "rootfs.ext4"))},
	}
	for i, path := range g.disks {
		requests = append(requests, struct{ path, body string }{
			fmt.Sprintf("/drives/disk%d", i),
			fmt.Sprintf(`{"drive_id":"disk%d","path_on_host":%q,"is_root_device":false,"is_read_only":false}`, i, path),
		})
	}
	requests = append(requests,
		struct{ path, body string }{"/network-interfaces/eth0", fmt.Sprintf(`{"iface_id":"eth0","guest_mac":%q,"host_dev_name":%q}`, g.spec.Network.MAC, tap)})
	for i, extraTap := range extraTaps {
		name := fmt.Sprintf("eth%d", i+1)
		requests = append(requests, struct{ path, body string }{"/network-interfaces/" + name,
			fmt.Sprintf(`{"iface_id":%q,"guest_mac":%q,"host_dev_name":%q}`, name, g.spec.Extra[i].MAC, extraTap)})
	}
	requests = append(requests,
		struct{ path, body string }{"/vsock", fmt.Sprintf(`{"guest_cid":3,"uds_path":%q}`, g.vsockPath())},
		struct{ path, body string }{"/actions", `{"action_type":"InstanceStart"}`},
	)
	for _, req := range requests {
		if err := firecrackerRequest(ctx, api, http.MethodPut, req.path, req.body); err != nil {
			kill()
			return nil, nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(g.dir, "firecracker.pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o644); err != nil {
		kill()
		return nil, nil, err
	}
	return cmd, ended, nil
}

func (g *guest) patchDrive(ctx context.Context, driveID, path string) error {
	api := filepath.Join(g.dir, "firecracker.socket")
	return firecrackerRequest(ctx, api, http.MethodPatch, "/drives/"+driveID, fmt.Sprintf(`{"drive_id":%q,"path_on_host":%q}`, driveID, path))
}

func (g *guest) vsockPath() string { return filepath.Join(g.dir, "v.sock") }

// waitForSocket waits for Firecracker's API socket to accept a connection,
// and gives up with Firecracker's own output when the process ends first.
// The socket file appears at bind, before Firecracker listens on it, so its
// existence alone does not mean a request can connect.
func waitForSocket(ctx context.Context, path string, timeout time.Duration, ended <-chan struct{}, consolePath string) error {
	deadline := time.Now().Add(timeout)
	for {
		var d net.Dialer
		if conn, err := d.DialContext(ctx, "unix", path); err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("firecracker did not open %s within %s", path, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ended:
			output, _ := os.ReadFile(consolePath)
			return fmt.Errorf("firecracker exited before opening %s: %s", path, strings.TrimSpace(string(output)))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func firecrackerRequest(ctx context.Context, socket, method, path, payload string) error {
	// Firecracker's API server admits a handful of connections at once and
	// answers 503 "Too many open connections" past that, so no request may
	// leave an idle keep-alive connection behind.
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var fault struct {
			Message string `json:"fault_message"`
		}
		_ = json.Unmarshal(body, &fault)
		if fault.Message == "" {
			fault.Message = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("firecracker %s %s: HTTP %d: %s", method, path, resp.StatusCode, fault.Message)
	}
	return nil
}

// vsockClient reaches guestinit's control port through Firecracker's vsock
// device: a connection to the device's Unix socket, opened with the port the
// guest listens on.
func (g *guest) vsockClient() *http.Client {
	path := g.vsockPath()
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", path)
			if err != nil {
				return nil, err
			}
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
			} else {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			}
			if _, err := fmt.Fprintf(conn, "CONNECT %d\n", controlPort); err != nil {
				_ = conn.Close()
				return nil, err
			}
			var reply [32]byte
			n, err := conn.Read(reply[:])
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			if !strings.HasPrefix(string(reply[:n]), "OK ") {
				_ = conn.Close()
				return nil, fmt.Errorf("vsock connect: %q", strings.TrimSpace(string(reply[:n])))
			}
			_ = conn.SetDeadline(time.Time{})
			return conn, nil
		},
	}}
}
