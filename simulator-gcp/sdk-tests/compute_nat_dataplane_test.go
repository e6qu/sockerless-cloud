package gcp_sdk_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/structpb"
)

// outsideListener is a listener of the test's own on an address an instance
// reaches only by leaving its network: the host's own address, where the
// test records who connected.
func outsideListener(t *testing.T) (address string, lastRemote func() string) {
	t.Helper()
	probe, err := net.Dial("udp4", "192.0.2.1:9")
	require.NoError(t, err)
	host := probe.LocalAddr().(*net.UDPAddr).IP.String()
	_ = probe.Close()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var remote string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remote, _, _ = net.SplitHostPort(r.RemoteAddr)
		mu.Unlock()
		_, _ = io.WriteString(w, "outside\n")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return net.JoinHostPort(host, port), func() string {
		mu.Lock()
		defer mu.Unlock()
		return remote
	}
}

// An instance with no external address leaves its network through the Cloud
// NAT of its region's router: what it opens outside arrives from the NAT and
// not from the instance, and the NAT's logging records the connection under
// the nat_gateway resource with the instance that opened it.
func TestCompute_CloudNATCarriesAnInstancesConnections(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone, region = "nat-project", "us-central1-a", "us-central1"
	subnet := createInstanceSubnetwork(t, svc, project, region, "nat-net", "10.85.0.0/24")
	_, err := svc.Routers.Insert(project, region, &compute.Router{
		Name: "nat-router", Network: "projects/" + project + "/global/networks/nat-net",
		Nats: []*compute.RouterNat{{
			Name:                          "nat",
			NatIpAllocateOption:           "AUTO_ONLY",
			SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES",
			LogConfig:                     &compute.RouterNatLogConfig{Enable: true, Filter: "ALL"},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Routers.Delete(project, region, "nat-router").Do() })

	outside, lastRemote := outsideListener(t)
	// The image carries bash and no curl: the request goes through bash's
	// own TCP device.
	script := fmt.Sprintf("#!/bin/bash\nif timeout 5 bash -c 'exec 3<>/dev/tcp/%s && printf \"GET / HTTP/1.0\\r\\n\\r\\n\" >&3 && cat <&3 >/dev/null'; then echo rc=0; else echo rc=1; fi\n", strings.Replace(outside, ":", "/", 1))
	op, err := svc.Instances.Insert(project, zone, &compute.Instance{
		Name:        "nat-vm",
		MachineType: "e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot: true, AutoDelete: true,
			InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: "projects/debian-cloud/global/images/family/" + debianFamily()},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{Subnetwork: subnet}},
		Metadata:          &compute.Metadata{Items: []*compute.MetadataItems{{Key: "startup-script", Value: googleapi.String(script)}}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, "nat-vm").Do() })
	done := awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the insert of nat-vm failed: %+v", done.Error)
	instance, err := svc.Instances.Get(project, zone, "nat-vm").Do()
	require.NoError(t, err)
	instanceIP := instance.NetworkInterfaces[0].NetworkIP

	console := awaitSerialPortOutput(t, svc, project, zone, "nat-vm", "startup-script exit status 0")
	require.Contains(t, console, "startup-script: rc=0", "the instance did not reach the outside through Cloud NAT")
	assert.NotEqual(t, instanceIP, lastRemote(), "the connection arrived from the instance's own address, not the NAT's")

	// The NAT logs each connection it translated to Cloud Logging, as the
	// connections happen.
	logs := logadminClientFor(t, project)
	var found bool
	require.Eventually(t, func() bool {
		it := logs.Entries(ctx, logadmin.Filter(`resource.type="nat_gateway"`))
		for {
			entry, err := it.Next()
			if err == iterator.Done {
				return found
			}
			require.NoError(t, err)
			// The client unescapes the log ID and hands a JSON payload over
			// as a protobuf Struct.
			if entry.Resource == nil || entry.Resource.Labels["gateway_name"] != "nat" || !strings.HasSuffix(entry.LogName, "/logs/compute.googleapis.com/nat_flows") {
				continue
			}
			jsonPayload, ok := entry.Payload.(*structpb.Struct)
			if !ok {
				continue
			}
			payload := jsonPayload.AsMap()
			connection, _ := payload["connection"].(map[string]any)
			endpoint, _ := payload["endpoint"].(map[string]any)
			if connection["src_ip"] == instanceIP && endpoint["vm_name"] == "nat-vm" && payload["allocation_status"] == "OK" {
				found = true
			}
		}
	}, time.Minute, 2*time.Second, "Cloud Logging holds no nat_flows entry for the instance's connection")
}
