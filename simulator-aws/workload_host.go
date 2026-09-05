package main

import (
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/e6qu/sockerless-cloud/sim"
)

func workloadCallbackHost() (string, error) {
	if runningInsideContainer() {
		if host := firstNonLoopbackIPv4(); host != "" {
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
	return "host.docker.internal", nil
}

func runningInsideContainer() bool {
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("container") != ""
}

func firstNonLoopbackIPv4() string {
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
