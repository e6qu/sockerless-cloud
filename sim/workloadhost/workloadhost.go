// Package workloadhost resolves the coordinates a workload container uses to
// reach the simulator that started it and the machine both run on.
package workloadhost

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const (
	dockerHostAlias     = "host.docker.internal"
	containersHostAlias = "host.containers.internal"
	hostGateway         = "host-gateway"
)

// InContainer reports whether the simulator process itself runs in a container.
func InContainer() bool {
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("container") != ""
}

func FirstNonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	return ""
}

func podmanRuntime() bool {
	return strings.Contains(strings.ToLower(sim.RuntimeInfo()), "podman")
}

// CallbackHost returns the host a workload container dials to reach this
// simulator's listeners. A containerized simulator is reached on its own
// address. A simulator on Linux listens in the host namespace, which workloads
// reach through the runtime's default bridge gateway. Desktop runtimes forward
// their host alias to the machine the simulator runs on.
func CallbackHost() (string, error) {
	if InContainer() {
		if host := FirstNonLoopbackIPv4(); host != "" {
			return host, nil
		}
		return "", fmt.Errorf("containerized simulator has no non-loopback IPv4 address")
	}
	if runtime.GOOS == "linux" {
		host, err := sim.DefaultContainerNetworkGatewayIPv4()
		if err != nil {
			return "", fmt.Errorf("resolve Linux workload callback gateway: %w", err)
		}
		return host, nil
	}
	if podmanRuntime() {
		return containersHostAlias, nil
	}
	return dockerHostAlias, nil
}

// ListenPort returns the TCP port of a listen address such as ":4566" or
// "0.0.0.0:4566".
func ListenPort(listenAddr string) (int, error) {
	port := listenAddr
	if idx := strings.LastIndex(listenAddr, ":"); idx >= 0 {
		port = listenAddr[idx+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid simulator listen port %q", port)
	}
	return n, nil
}

// CallbackAddr returns the host:port a workload container dials to reach the
// simulator listening on listenAddr.
func CallbackAddr(listenAddr string) (string, error) {
	port, err := ListenPort(listenAddr)
	if err != nil {
		return "", err
	}
	host, err := CallbackHost()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// OuterHostGatewayIPv4 returns the address of the machine that runs a
// containerized simulator's container runtime, as the simulator's container
// resolves it: the runtime's own host aliases first, the default route second.
func OuterHostGatewayIPv4() string {
	return outerHostGatewayIPv4(net.LookupHost, defaultRouteGatewayIPv4)
}

func outerHostGatewayIPv4(lookup func(string) ([]string, error), fallback func() string) string {
	for _, hostname := range []string{dockerHostAlias, containersHostAlias} {
		addresses, err := lookup(hostname)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip := net.ParseIP(strings.TrimSpace(address))
			if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
				continue
			}
			return ip.To4().String()
		}
	}
	return fallback()
}

func defaultRouteGatewayIPv4() string {
	content, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return parseDefaultRouteGatewayIPv4(string(content))
}

func parseDefaultRouteGatewayIPv4(content string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		gateway, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil || gateway == 0 {
			continue
		}
		return net.IPv4(
			byte(gateway),
			byte(gateway>>8),
			byte(gateway>>16),
			byte(gateway>>24),
		).String()
	}
	return ""
}

// OuterHostEntries returns the host aliases a containerized simulator's
// workloads resolve to the outer host. The runtime puts a nested workload on a
// different network from the simulator's, so the workload cannot share the
// simulator's alias resolution. Outside a container it returns nothing.
func OuterHostEntries() []sim.HostEntry {
	if !InContainer() {
		return nil
	}
	gateway := OuterHostGatewayIPv4()
	if gateway == "" {
		return nil
	}
	return []sim.HostEntry{
		{IP: gateway, Name: dockerHostAlias},
		{IP: gateway, Name: containersHostAlias},
	}
}

// ExtraHosts returns the container host entries a workload needs to resolve
// the outer host's aliases.
func ExtraHosts() []string {
	switch {
	case InContainer():
		var out []string
		for _, entry := range OuterHostEntries() {
			out = append(out, entry.Name+":"+entry.IP)
		}
		return out
	case podmanRuntime():
		// Podman resolves both host aliases itself and rejects host-gateway,
		// but a Podman machine's aliases name the virtual machine rather than
		// the desktop host the simulator runs on.
		if host := podmanMachineHost(); host != "" {
			return []string{containersHostAlias + ":" + host, dockerHostAlias + ":" + host}
		}
		return nil
	default:
		return []string{dockerHostAlias + ":" + hostGateway}
	}
}

// AliasHosts returns container host entries that resolve each of aliases, a
// cloud's metadata server names, to the simulator's callback host.
func AliasHosts(aliases ...string) ([]string, error) {
	target, err := CallbackHost()
	if err != nil {
		return nil, err
	}
	if net.ParseIP(target) == nil {
		if podmanRuntime() {
			target = podmanMachineHost()
			if target == "" {
				return nil, fmt.Errorf("resolve the Podman machine's desktop host address for %s", strings.Join(aliases, ", "))
			}
		} else {
			target = hostGateway
		}
	}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		out = append(out, alias+":"+target)
	}
	return out, nil
}

func podmanMachineHost() string {
	if runtime.GOOS == "linux" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "machine", "ssh", "--", "ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	return parsePodmanMachineHostIPv4(string(out))
}

func parsePodmanMachineHostIPv4(route string) string {
	for _, line := range strings.Split(route, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "src" {
				continue
			}
			ip := net.ParseIP(fields[i+1]).To4()
			if ip == nil {
				continue
			}
			// Podman machine user-mode networking exposes the desktop host at
			// the last usable address of the virtual machine's subnet.
			return net.IPv4(ip[0], ip[1], ip[2], 254).String()
		}
	}
	return ""
}

// MergeEnv returns a new map holding every key of envs, later maps winning.
func MergeEnv(envs ...map[string]string) map[string]string {
	n := 0
	for _, env := range envs {
		n += len(env)
	}
	out := make(map[string]string, n)
	for _, env := range envs {
		for k, v := range env {
			out[k] = v
		}
	}
	return out
}

// MetadataIndex maps a workload's private IPv4 address to the instance record
// a metadata server answers that workload's requests with.
type MetadataIndex[T any] struct {
	m sync.Map
}

func (x *MetadataIndex[T]) Store(ip string, v T) { x.m.Store(ip, v) }

func (x *MetadataIndex[T]) Delete(ip string) { x.m.Delete(ip) }

// ForRequest returns the record stored for the request's source address.
func (x *MetadataIndex[T]) ForRequest(r *http.Request) (T, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	v, ok := x.m.Load(host)
	if !ok {
		var zero T
		return zero, false
	}
	rec, ok := v.(T)
	return rec, ok
}
