package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	dockerclient "github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// cloudRunDefaultContainerPort is the port Cloud Run sends requests to, and
// passes in PORT, when a container declares none.
const cloudRunDefaultContainerPort = 8080

// cloudRunPublishedPort is the one container port sim.StartHTTPContainer
// publishes on the host's loopback.
const cloudRunPublishedPort = 8080

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

// cloudRunContainerRoute returns the address this host reaches a container's
// port at. The container's own address is used whenever this host routes to
// it, which a connection the container accepts or refuses proves; a host that
// does not route container addresses (Docker Desktop, rootless Podman) reaches
// the workload only through the port the engine publishes on loopback.
func cloudRunContainerRoute(ctx context.Context, containerID string, port int) (string, error) {
	if ip := sim.ContainerIPv4(containerID); ip != "" {
		addr := net.JoinHostPort(ip, strconv.Itoa(port))
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return addr, nil
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return addr, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if port != cloudRunPublishedPort {
		return "", fmt.Errorf("container %s listens on port %d, and this host reaches a container only through its published port %d", containerID, port, cloudRunPublishedPort)
	}
	hostPort, err := sim.PublishedHostPort(ctx, containerID, cloudRunPublishedPort)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), nil
}

// probeTarget is a probed container in the instance's network namespace:
// route reaches routePort there, and port is the probed container's own port,
// which an action that names no port probes.
type probeTarget struct {
	route     string
	routePort int
	port      int
}

// address is the address a probe action dials.
func (t probeTarget) address(actionPort int32) (string, error) {
	port := t.port
	if actionPort > 0 {
		port = int(actionPort)
	}
	if port == t.routePort {
		return t.route, nil
	}
	host, _, err := net.SplitHostPort(t.route)
	if err != nil {
		return "", err
	}
	if host == "127.0.0.1" {
		return "", fmt.Errorf("probe port %d is reachable only at the container's own address, which this host does not route to", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// runStartupProbe runs a container's startup probe until it succeeds, fails
// failureThreshold times in a row, the container exits, or ctx ends. route is
// the address routePort is reached at; containerPort is the probed
// container's own port.
func runStartupProbe(ctx context.Context, p Probe, route string, routePort, containerPort int, exited <-chan struct{}) error {
	target := probeTarget{route: route, routePort: routePort, port: containerPort}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-exited:
			cancel()
		case <-ctx.Done():
		}
	}()
	stopped := func(err error) error {
		select {
		case <-exited:
			return errors.New("the container exited before its startup probe succeeded")
		default:
		}
		return err
	}

	if p.InitialDelaySeconds > 0 {
		select {
		case <-ctx.Done():
			return stopped(ctx.Err())
		case <-time.After(time.Duration(p.InitialDelaySeconds) * time.Second):
		}
	}
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	period := time.Duration(p.PeriodSeconds) * time.Second
	var lastErr error
	for failures := 0; ; {
		started := time.Now()
		lastErr = probeOnce(ctx, p, target, timeout)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return stopped(fmt.Errorf("%w: %w", ctx.Err(), lastErr))
		}
		failures++
		if failures >= int(p.FailureThreshold) {
			return fmt.Errorf("startup probe failed %d time(s): %w", failures, lastErr)
		}
		select {
		case <-ctx.Done():
			return stopped(fmt.Errorf("%w: %w", ctx.Err(), lastErr))
		case <-time.After(time.Until(started.Add(period))):
		}
	}
}

// probeOnce makes one probe attempt, bounded by the probe's timeout.
func probeOnce(ctx context.Context, p Probe, target probeTarget, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch {
	case p.HTTPGet != nil:
		addr, err := target.address(p.HTTPGet.Port)
		if err != nil {
			return err
		}
		return probeHTTPGet(ctx, addr, p.HTTPGet)
	case p.GRPC != nil:
		addr, err := target.address(p.GRPC.Port)
		if err != nil {
			return err
		}
		return probeGRPC(ctx, addr, p.GRPC.Service)
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
	dialer := &net.Dialer{Timeout: time.Second}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("TCP probe on %s: %w", addr, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// probeHTTPGet succeeds when the container answers the GET with a status from
// 200 to 399.
func probeHTTPGet(ctx context.Context, addr string, action *HTTPGetAction) error {
	path := action.Path
	if path == "" {
		path = "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return err
	}
	for _, h := range action.HTTPHeaders {
		req.Header.Add(h.Name, h.Value)
	}
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP probe GET %s: %w", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP probe GET %s answered %d", path, resp.StatusCode)
	}
	return nil
}

// probeGRPC succeeds when the container's gRPC health service reports the
// named service SERVING.
func probeGRPC(ctx context.Context, addr, service string) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		return fmt.Errorf("gRPC health probe: %w", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("gRPC health probe: service %q is %s", service, resp.GetStatus())
	}
	return nil
}

// watchCloudRunContainerExit returns a channel the engine's wait closes when
// the container stops running, and the function that releases the wait.
func watchCloudRunContainerExit(containerID string) (<-chan struct{}, context.CancelFunc) {
	exited := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cli := sim.DockerClient()
	if cli == nil {
		close(exited)
		return exited, cancel
	}
	wait := cli.ContainerWait(ctx, containerID, dockerclient.ContainerWaitOptions{Condition: "not-running"})
	go func() {
		select {
		case <-wait.Result:
			close(exited)
		case err := <-wait.Error:
			if ctx.Err() == nil && err != nil {
				close(exited)
			}
		case <-ctx.Done():
		}
	}()
	return exited, cancel
}

// awaitExit waits for the first of the instance's containers to stop and
// returns its exit code, or -1 when the engine reports none.
func (inst *cloudRunServiceInstance) awaitExit(ctx context.Context) int64 {
	inst.mu.Lock()
	ids := []string{inst.ownerID}
	for _, h := range inst.handles {
		ids = append(ids, h.ContainerID)
	}
	inst.mu.Unlock()
	cli := sim.DockerClient()
	if cli == nil {
		return -1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	codes := make(chan int64, len(ids))
	for _, id := range ids {
		wait := cli.ContainerWait(ctx, id, dockerclient.ContainerWaitOptions{Condition: "not-running"})
		go func() {
			select {
			case result := <-wait.Result:
				codes <- result.StatusCode
			case err := <-wait.Error:
				if ctx.Err() == nil && err != nil {
					codes <- -1
				}
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
