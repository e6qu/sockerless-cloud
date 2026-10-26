package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/replicas"
)

// cloudRunDefaultContainerPort is the port Cloud Run sends requests to, and
// passes in PORT, when a container declares none.
const cloudRunDefaultContainerPort = 8080

// cloudRunContainerPort is the port a container receives requests on:
// ports[0].containerPort, or 8080.
func cloudRunContainerPort(c Container) int {
	if len(c.Ports) > 0 && c.Ports[0].ContainerPort > 0 {
		return int(c.Ports[0].ContainerPort)
	}
	return cloudRunDefaultContainerPort
}

// cloudRunStartupProbe is the startup probe Cloud Run runs against a container:
// the one it configures, with the API's documented defaults filled in, or the
// TCP probe on the container port Cloud Run applies to a container that
// configures none (timeout and period 240 seconds, failure threshold 1).
func cloudRunStartupProbe(c Container) Probe {
	if c.StartupProbe == nil {
		return Probe{
			TimeoutSeconds:   240,
			PeriodSeconds:    240,
			FailureThreshold: 1,
			TCPSocket:        &TCPSocketAction{},
		}
	}
	p := *c.StartupProbe
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 1
	}
	if p.PeriodSeconds <= 0 {
		p.PeriodSeconds = 10
	}
	if p.FailureThreshold <= 0 {
		p.FailureThreshold = 3
	}
	return p
}

// cloudRunContainerRoute returns the address this host reaches a port of the
// instance's namespace at, without connecting to it: Cloud Run opens no
// connection to a workload but its probes and requests. The owner's own
// address is used when one of this host's interfaces is on its network, as a
// Linux engine's bridge is; a host that is not (Docker Desktop, rootless
// Podman) reaches the workload only through the port the engine publishes on
// loopback.
func cloudRunContainerRoute(ctx context.Context, owner placement.Handle, port int) (string, error) {
	if ip, err := owner.Address(ctx); err == nil && ip != "" {
		onNetwork, err := cloudRunHostOnNetworkOf(ip)
		if err != nil {
			return "", err
		}
		if onNetwork {
			return net.JoinHostPort(ip, strconv.Itoa(port)), nil
		}
	}
	hostPort, err := owner.PublishedPort(ctx, port)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), nil
}

// cloudRunHostOnNetworkOf reports whether one of this host's interfaces is on
// the network that holds ip.
func cloudRunHostOnNetworkOf(ip string) (bool, error) {
	address := net.ParseIP(ip)
	if address == nil {
		return false, fmt.Errorf("container address %q is not an IP address", ip)
	}
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return false, fmt.Errorf("list this host's interface addresses: %w", err)
	}
	for _, assigned := range interfaces {
		if network, ok := assigned.(*net.IPNet); ok && !network.IP.IsLoopback() && network.Contains(address) {
			return true, nil
		}
	}
	return false, nil
}

// probeTarget is a probed container in the instance's network namespace:
// route gives the address this host reaches a port of that namespace at, and
// port is the probed container's own port, which an action that names no port
// probes.
type probeTarget struct {
	route func(port int) (string, error)
	port  int
}

// address is the address a probe action dials.
func (t probeTarget) address(actionPort int32) (string, error) {
	return t.route(probeActionPort(actionPort, t.port))
}

func probeActionPort(actionPort int32, containerPort int) int {
	if actionPort > 0 {
		return int(actionPort)
	}
	return containerPort
}

// cloudRunNamespacePorts is every port of an instance's network namespace this
// host reaches: the ingress container's port when the workload serves
// requests, and the port of each startup probe a container runs. The owner of
// the namespace publishes them all, since a host that routes no container
// address reaches a port only where the engine publishes it.
func cloudRunNamespacePorts(containers []Container, serves bool) []int {
	seen := map[int]bool{}
	var ports []int
	add := func(port int) {
		if !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	if serves {
		add(cloudRunContainerPort(containers[0]))
	}
	for i, c := range containers {
		if c.StartupProbe == nil && (!serves || i != 0) {
			continue
		}
		p := cloudRunStartupProbe(c)
		own := cloudRunContainerPort(c)
		switch {
		case p.HTTPGet != nil:
			add(probeActionPort(p.HTTPGet.Port, own))
		case p.GRPC != nil:
			add(probeActionPort(p.GRPC.Port, own))
		case p.TCPSocket != nil:
			add(probeActionPort(p.TCPSocket.Port, own))
		default:
			add(own)
		}
	}
	return ports
}

// runStartupProbe runs a container's startup probe until it succeeds, fails
// failureThreshold times in a row, the container exits, or ctx ends. route
// gives the address a port of the container's network namespace is reached
// at; containerPort is the probed container's own port.
func runStartupProbe(ctx context.Context, p Probe, route func(int) (string, error), containerPort int, exited <-chan struct{}) error {
	target := probeTarget{route: route, port: containerPort}
	seconds := func(n int32) time.Duration { return time.Duration(n) * time.Second }
	probe := replicas.Probe{
		Check:            func(ctx context.Context) error { return probeOnce(ctx, p, target, seconds(p.TimeoutSeconds)) },
		InitialDelay:     seconds(p.InitialDelaySeconds),
		Period:           seconds(p.PeriodSeconds),
		Timeout:          seconds(p.TimeoutSeconds),
		FailureThreshold: int(p.FailureThreshold),
	}
	err := probe.Await(ctx, time.Now(), exited)
	if errors.Is(err, replicas.ErrExited) {
		return errors.New("the container exited before its startup probe succeeded")
	}
	return err
}

// probeOnce makes one probe attempt; ctx ends when the probe's timeout does.
func probeOnce(ctx context.Context, p Probe, target probeTarget, timeout time.Duration) error {
	switch {
	case p.HTTPGet != nil:
		addr, err := target.address(p.HTTPGet.Port)
		if err != nil {
			return err
		}
		probe := lbplane.HTTPProbe{Scheme: "http", Address: addr, Path: p.HTTPGet.Path, Header: http.Header{},
			Timeout: timeout, Match: lbplane.StatusRange(200, 399)}
		for _, h := range p.HTTPGet.HTTPHeaders {
			if strings.EqualFold(h.Name, "Host") {
				probe.Host = h.Value
				continue
			}
			probe.Header.Add(h.Name, h.Value)
		}
		_, err = lbplane.ProbeHTTP(ctx, probe)
		return err
	case p.GRPC != nil:
		addr, err := target.address(p.GRPC.Port)
		if err != nil {
			return err
		}
		return lbplane.ProbeGRPC(ctx, addr, p.GRPC.Service, timeout)
	default:
		var port int32
		if p.TCPSocket != nil {
			port = p.TCPSocket.Port
		}
		addr, err := target.address(port)
		if err != nil {
			return err
		}
		return probeTCP(ctx, addr)
	}
}

// probeTCP succeeds once the address accepts a connection, retrying a refused
// connection until the attempt's timeout: Cloud Run's default probe waits up to
// its 240-second timeout for the port to open.
func probeTCP(ctx context.Context, addr string) error {
	for {
		err := lbplane.ProbeTCP(ctx, addr, time.Second)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("TCP probe on %s: %w", addr, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// exited reports the exit code of a container of the instance that has
// already exited, without waiting for one.
func (inst *cloudRunServiceInstance) exited() (int64, bool) {
	inst.mu.Lock()
	handles := append([]placement.Handle(nil), inst.handles...)
	inst.mu.Unlock()
	for _, h := range handles {
		select {
		case <-h.Exited():
			result := h.Wait()
			if result.Error != nil && result.ExitCode < 0 {
				return -1, true
			}
			return int64(result.ExitCode), true
		default:
		}
	}
	return 0, false
}

// awaitExit waits for the first of the instance's containers to stop and
// returns its exit code, or -1 when the backend reports none.
func (inst *cloudRunServiceInstance) awaitExit(ctx context.Context) int64 {
	inst.mu.Lock()
	handles := append([]placement.Handle(nil), inst.handles...)
	inst.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	codes := make(chan int64, len(handles))
	for _, h := range handles {
		go func() {
			select {
			case <-h.Exited():
				result := h.Wait()
				if result.Error != nil && result.ExitCode < 0 {
					codes <- -1
					return
				}
				codes <- int64(result.ExitCode)
			case <-ctx.Done():
			}
		}()
	}
	select {
	case code := <-codes:
		return code
	case <-ctx.Done():
		return -1
	}
}
