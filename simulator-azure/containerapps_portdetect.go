package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Container Apps reserves this port for its own health checks.
const acaReservedHealthPort = 36985

const (
	acaPortScanWorkers     = 2048
	acaPortScanDialTimeout = 250 * time.Millisecond
)

// acaListeningPorts dials every TCP port of a replica and returns those that
// accept, as Container Apps scans a container whose HTTP ingress names no
// target port.
func acaListeningPorts(ctx context.Context, host string) []int32 {
	ports := make(chan int)
	var (
		mu    sync.Mutex
		found []int32
		wg    sync.WaitGroup
	)
	dialer := net.Dialer{Timeout: acaPortScanDialTimeout}
	for range acaPortScanWorkers {
		wg.Go(func() {
			for port := range ports {
				conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
				if err != nil {
					continue
				}
				_ = conn.Close()
				mu.Lock()
				found = append(found, int32(port))
				mu.Unlock()
			}
		})
	}
	for port := 1; port <= 65535 && ctx.Err() == nil; port++ {
		if port != acaReservedHealthPort {
			ports <- port
		}
	}
	close(ports)
	wg.Wait()
	return found
}

// acaDetectedTargetPort is the one port a replica listens on; with none or
// several, Container Apps sets no target port.
func acaDetectedTargetPort(ctx context.Context, host string) (int32, error) {
	ports := acaListeningPorts(ctx, host)
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("scan the container's ports: %w", err)
	}
	switch len(ports) {
	case 1:
		return ports[0], nil
	case 0:
		return 0, fmt.Errorf("ingress names no target port and the container listens on no port")
	default:
		return 0, fmt.Errorf("ingress names no target port and the container listens on %d ports %v; set the target port", len(ports), ports)
	}
}

// Detected ports by replica address. A cached port that no longer accepts
// is scanned again, so an address a later replica reuses is not misread.
var (
	acaDetectedPortsMu sync.Mutex
	acaDetectedPorts   = map[string]int32{}
)

func acaReplicaTargetPort(ctx context.Context, app ContainerApp, host string) (int32, error) {
	if port, ok := acaIngressTargetPort(app); ok {
		return port, nil
	}
	return acaDetectedPortCached(ctx, host)
}

func acaDetectedPortCached(ctx context.Context, host string) (int32, error) {
	acaDetectedPortsMu.Lock()
	port, ok := acaDetectedPorts[host]
	acaDetectedPortsMu.Unlock()
	if ok {
		conn, err := (&net.Dialer{Timeout: acaPortScanDialTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err == nil {
			_ = conn.Close()
			return port, nil
		}
	}
	port, err := acaDetectedTargetPort(ctx, host)
	acaDetectedPortsMu.Lock()
	defer acaDetectedPortsMu.Unlock()
	if err != nil {
		delete(acaDetectedPorts, host)
		return 0, err
	}
	acaDetectedPorts[host] = port
	return port, nil
}
