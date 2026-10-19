package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

// EgressSpec places one egress sidecar: a cloud's NAT gateway from inside
// the network, at the gateway's address, carrying the connections the
// sources open toward the outside from that address.
type EgressSpec struct {
	// Network is the engine network the gateway serves.
	Network string
	// Address is the gateway's address inside the network's range.
	Address netip.Addr
	// Name is the container's name.
	Name string
	// Gateway names the gateway in the sidecar's reports.
	Gateway string
	// Sources are the ranges whose connections the gateway carries.
	Sources []netip.Prefix
	// Local are the ranges a connection stays inside the network for; the
	// network's own range at least.
	Local []netip.Prefix
	// TCPIdle and UDPIdle are the gateway's idle timeouts.
	TCPIdle time.Duration
	UDPIdle time.Duration
	// HTTPUpstream is the simulator's HTTP origin as the sidecar reaches it,
	// where the reports go.
	HTTPUpstream string
	// Token is the credential the simulator minted for this sidecar.
	Token      string
	ExtraHosts []string
	Labels     map[string]string
}

// StartEgress runs an egress sidecar and returns its handle once it carries
// connections, or when ctx ends first; the container goes with the handle's
// Cancel. The sidecar programs the redirect in its own network namespace,
// which the one capability it keeps allows and nothing else.
func StartEgress(ctx context.Context, spec EgressSpec, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if spec.Network == "" || !spec.Address.IsValid() || spec.Gateway == "" || len(spec.Sources) == 0 || spec.HTTPUpstream == "" || spec.Token == "" {
		return nil, errors.New("sidecar: an egress sidecar needs a network, an address, a gateway, sources, the HTTP upstream and a token")
	}
	if spec.TCPIdle <= 0 || spec.UDPIdle <= 0 {
		return nil, errors.New("sidecar: an egress sidecar needs the gateway's idle timeouts")
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting an egress sidecar"); err != nil {
		return nil, err
	}
	image, platform, err := ensureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"sockerless-sim-egress": spec.Network}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	serving := make(chan struct{})
	var once sync.Once
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         spec.Name,
		Network:      spec.Network,
		IPv4Address:  spec.Address.String(),
		Args:         []string{"egress"},
		ExtraHosts:   spec.ExtraHosts,
		Labels:       labels,
		Env: map[string]string{
			egress.EnvSources:       joinPrefixes(spec.Sources),
			egress.EnvLocal:         joinPrefixes(spec.Local),
			egress.EnvTCPIdle:       strconv.Itoa(int(spec.TCPIdle / time.Second)),
			egress.EnvUDPIdle:       strconv.Itoa(int(spec.UDPIdle / time.Second)),
			egress.EnvGateway:       spec.Gateway,
			"SIDECAR_HTTP_UPSTREAM": spec.HTTPUpstream,
			"SIDECAR_TOKEN":         spec.Token,
		},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_ADMIN"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
			ReadonlyRootfs:   true,
		},
	}, sim.FuncSink(func(line sim.LogLine) {
		// The caller's sink holds the line before the start returns.
		sink.WriteLog(line)
		if strings.HasPrefix(line.Text, "sidecar: egress ") {
			once.Do(func() { close(serving) })
		}
	}))
	if err != nil {
		return nil, err
	}
	select {
	case <-serving:
		return handle, nil
	case <-handle.Exited():
		handle.Cancel()
		return nil, fmt.Errorf("sidecar: the egress sidecar %s ended before it served", spec.Name)
	case <-ctx.Done():
		handle.Cancel()
		return nil, ctx.Err()
	}
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

// EgressReports is the handler a simulator mounts at egress.ReportPath: it
// hands each report to take with the network whose sidecar sent it, which
// the sidecar's token establishes, and refuses a report from anything else.
func EgressReports(take func(network string, report egress.Report)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		network := sim.SidecarNetwork(r.Context())
		if network == "" {
			http.Error(w, "only a network's sidecar reports here", http.StatusUnauthorized)
			return
		}
		var report egress.Report
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&report); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		take(network, report)
		w.WriteHeader(http.StatusNoContent)
	}
}
