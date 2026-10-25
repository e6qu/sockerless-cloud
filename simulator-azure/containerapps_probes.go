package main

import (
	"context"
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

const (
	acaProbeStartup   = "Startup"
	acaProbeReadiness = "Readiness"
	acaProbeLiveness  = "Liveness"
)

// acaProbe is one probe of a container with Kubernetes' defaults filled in.
type acaProbe struct {
	kind         string
	http         *ContainerAppProbeHTTPGet
	tcp          *ContainerAppProbeTCPSocket
	initialDelay time.Duration
	period       time.Duration
	timeout      time.Duration
	failures     int
	successes    int
}

func newACAProbe(p ContainerAppProbe) acaProbe {
	probe := acaProbe{kind: acaProbeKind(p.Type), http: p.HTTPGet, tcp: p.TCPSocket,
		period: 10 * time.Second, timeout: time.Second, failures: 3, successes: 1}
	seconds := func(v *int32, into *time.Duration) {
		if v != nil {
			*into = time.Duration(*v) * time.Second
		}
	}
	seconds(p.InitialDelaySeconds, &probe.initialDelay)
	seconds(p.PeriodSeconds, &probe.period)
	seconds(p.TimeoutSeconds, &probe.timeout)
	if p.FailureThreshold != nil {
		probe.failures = int(*p.FailureThreshold)
	}
	if p.SuccessThreshold != nil {
		probe.successes = int(*p.SuccessThreshold)
	}
	return probe
}

func acaProbeKind(t string) string {
	for _, kind := range []string{acaProbeStartup, acaProbeReadiness, acaProbeLiveness} {
		if strings.EqualFold(t, kind) {
			return kind
		}
	}
	return t
}

// acaDefaultProbes are the probes Container Apps adds to the container its
// ingress targets for each type the template leaves unset: TCP connections
// to the target port.
func acaDefaultProbes(port int32) map[string]acaProbe {
	tcp := &ContainerAppProbeTCPSocket{Port: port}
	return map[string]acaProbe{
		acaProbeStartup: {kind: acaProbeStartup, tcp: tcp, initialDelay: time.Second, period: time.Second,
			timeout: 3 * time.Second, failures: 240, successes: 1},
		acaProbeReadiness: {kind: acaProbeReadiness, tcp: tcp, initialDelay: 3 * time.Second, period: 5 * time.Second,
			timeout: 5 * time.Second, failures: 48, successes: 1},
		acaProbeLiveness: {kind: acaProbeLiveness, tcp: tcp, period: 10 * time.Second,
			timeout: time.Second, failures: 3, successes: 1},
	}
}

// acaContainerProbes returns a container's probes by type: the template's,
// and for the container ingress targets, the platform's defaults for each
// type the template leaves unset.
func acaContainerProbes(app ContainerApp, c JobContainer, ingressTarget bool) map[string]acaProbe {
	probes := map[string]acaProbe{}
	for _, p := range c.Probes {
		probe := newACAProbe(p)
		probes[probe.kind] = probe
	}
	if ingressTarget && acaIngressEnabled(app) {
		for kind, probe := range acaDefaultProbes(acaIngressTargetPort(app)) {
			if _, set := probes[kind]; !set {
				probes[kind] = probe
			}
		}
	}
	return probes
}

// acaValidateProbes checks a template's probes as the service does: each
// names its type once per container and one handler, inside the API's
// bounds.
func acaValidateProbes(t *ContainerAppTemplate) error {
	if t == nil {
		return nil
	}
	within := func(container, kind, field string, v *int32, lo, hi int32) error {
		if v != nil && (*v < lo || *v > hi) {
			return fmt.Errorf("container '%s' %s probe %s must be between %d and %d, got %d", container, kind, field, lo, hi, *v)
		}
		return nil
	}
	for _, c := range append(append([]JobContainer(nil), t.InitContainers...), t.Containers...) {
		seen := map[string]bool{}
		for _, p := range c.Probes {
			kind := acaProbeKind(p.Type)
			switch kind {
			case acaProbeStartup, acaProbeReadiness, acaProbeLiveness:
			default:
				return fmt.Errorf("container '%s' has a probe of type '%s'; the type is Liveness, Readiness or Startup", c.Name, p.Type)
			}
			if seen[kind] {
				return fmt.Errorf("container '%s' has more than one %s probe", c.Name, kind)
			}
			seen[kind] = true
			if (p.HTTPGet == nil) == (p.TCPSocket == nil) {
				return fmt.Errorf("container '%s' %s probe sets exactly one of httpGet and tcpSocket", c.Name, kind)
			}
			var port int32
			if p.HTTPGet != nil {
				port = p.HTTPGet.Port
				if s := p.HTTPGet.Scheme; s != "" && !strings.EqualFold(s, "HTTP") && !strings.EqualFold(s, "HTTPS") {
					return fmt.Errorf("container '%s' %s probe scheme must be HTTP or HTTPS, got '%s'", c.Name, kind, s)
				}
			} else {
				port = p.TCPSocket.Port
			}
			if port < 1 || port > 65535 {
				return fmt.Errorf("container '%s' %s probe port must be between 1 and 65535, got %d", c.Name, kind, port)
			}
			if p.SuccessThreshold != nil && *p.SuccessThreshold != 1 && kind != acaProbeReadiness {
				return fmt.Errorf("container '%s' %s probe successThreshold must be 1", c.Name, kind)
			}
			for _, err := range []error{
				within(c.Name, kind, "initialDelaySeconds", p.InitialDelaySeconds, 1, 60),
				within(c.Name, kind, "periodSeconds", p.PeriodSeconds, 1, 240),
				within(c.Name, kind, "timeoutSeconds", p.TimeoutSeconds, 1, 240),
				within(c.Name, kind, "failureThreshold", p.FailureThreshold, 1, 10),
				within(c.Name, kind, "successThreshold", p.SuccessThreshold, 1, 10),
			} {
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// check runs the probe once against the pod at address as the kubelet does:
// straight to the container, a redirect counting as success rather than being
// followed, and an HTTPS probe not verifying the container's certificate.
func (p acaProbe) check(ctx context.Context, address string) error {
	if p.tcp != nil {
		host := p.tcp.Host
		if host == "" {
			host = address
		}
		return lbplane.ProbeTCP(ctx, net.JoinHostPort(host, strconv.Itoa(int(p.tcp.Port))), p.timeout)
	}
	host := p.http.Host
	if host == "" {
		host = address
	}
	scheme := strings.ToLower(p.http.Scheme)
	if scheme == "" {
		scheme = "http"
	}
	probe := lbplane.HTTPProbe{Scheme: scheme, Address: net.JoinHostPort(host, strconv.Itoa(int(p.http.Port))),
		Path: p.http.Path, Header: http.Header{}, Timeout: p.timeout, Match: lbplane.StatusRange(200, 399)}
	for _, h := range p.http.HTTPHeaders {
		if strings.EqualFold(h.Name, "Host") {
			probe.Host = h.Value
			continue
		}
		probe.Header.Add(h.Name, h.Value)
	}
	_, err := lbplane.ProbeHTTP(ctx, probe)
	return err
}

// runner is the probe as the replica's probe runner runs it against the pod
// at address.
func (p acaProbe) runner(address string) *replicas.Probe {
	return &replicas.Probe{
		Check:            func(ctx context.Context) error { return p.check(ctx, address) },
		InitialDelay:     p.initialDelay,
		Period:           p.period,
		Timeout:          p.timeout,
		FailureThreshold: p.failures,
		SuccessThreshold: p.successes,
	}
}

// acaReplicaProbes are a container's probes run against the pod at address.
func acaReplicaProbes(probes map[string]acaProbe, address string) replicas.Probes {
	var out replicas.Probes
	for kind, into := range map[string]**replicas.Probe{
		acaProbeStartup: &out.Startup, acaProbeReadiness: &out.Readiness, acaProbeLiveness: &out.Liveness,
	} {
		if probe, ok := probes[kind]; ok {
			*into = probe.runner(address)
		}
	}
	return out
}

// startProbes runs a container's startup probe, then its readiness probe
// until it first passes, which together make the container started and
// ready; from there its readiness probe keeps its readiness current and its
// liveness probe restarts the pod, through restart, once it fails.
func (c *acaReplicaContainer) startProbes(ctx context.Context, probes map[string]acaProbe, member, owner placement.Handle, restart func()) error {
	started := time.Now()
	if len(probes) == 0 {
		c.setReady(true)
		return nil
	}
	address, err := owner.Address(ctx)
	if err != nil {
		select {
		case <-member.Exited():
			return fmt.Errorf("%w before its probes passed", replicas.ErrExited)
		default:
		}
		return fmt.Errorf("the pod has no address: %w", err)
	}
	return acaReplicaProbes(probes, address).Run(ctx, started, member.Exited(), c.setReady, restart)
}

// watchProbes keeps a running container's readiness and liveness probes.
func (c *acaReplicaContainer) watchProbes(probes map[string]acaProbe, member placement.Handle, address string, restart func()) {
	acaReplicaProbes(probes, address).Watch(member.Exited(), c.setReady, restart)
}
