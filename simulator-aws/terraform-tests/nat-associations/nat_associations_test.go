package natassociations_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestNATAssociationsTerraform applies a VPC whose NAT route exists before the
// aws_route_table_association that puts a subnet behind it, and whose main
// route table sends a subnet with no association through the same NAT
// gateway. An Amazon ECS service in each subnet reports to a probe on the
// host, which must see the NAT gateway's Elastic IP address as the source.
func TestNATAssociationsTerraform(t *testing.T) {
	requireNetnsFabric(t)
	probe, seen := startSourceProbe(t)
	t.Setenv("TF_VAR_probe", probe)
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	t.Cleanup(func() { env.Terraform(t, "destroy", "-auto-approve") })

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	natIP := outputs["nat_public_ip"].Value
	require.NotEmpty(t, natIP)

	want := map[string]bool{"explicit": true, "implicit": true}
	guard := time.After(3 * time.Minute)
	for len(want) > 0 {
		select {
		case got := <-seen:
			if !want[got[0]] {
				continue
			}
			require.Equal(t, natIP, got[1], "the %s subnet's task must leave the VPC from the NAT gateway's address", got[0])
			delete(want, got[0])
		case <-guard:
			t.Fatalf("no probe request arrived from %v", want)
		}
	}
}

// requireNetnsFabric gates the test on the kernel offering this process
// network namespaces, which the real VPC fabric is built from.
func requireNetnsFabric(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the real netns fabric needs a Linux kernel")
	}
	body, err := os.ReadFile("/proc/self/status")
	require.NoError(t, err)
	var caps uint64
	for _, line := range strings.Split(string(body), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "CapEff:" {
			caps, err = strconv.ParseUint(fields[1], 16, 64)
			require.NoError(t, err)
		}
	}
	const capNetAdmin, capSysAdmin = 12, 21
	if caps&(1<<capNetAdmin) == 0 || caps&(1<<capSysAdmin) == 0 {
		t.Skip("platform gate: the real netns fabric needs CAP_NET_ADMIN and CAP_SYS_ADMIN")
	}
	for _, bin := range []string{"ip", "nft", "nsenter", "sysctl"} {
		_, err := exec.LookPath(bin)
		require.NoError(t, err, "this host can run the netns fabric but is missing %s", bin)
	}
}

// startSourceProbe listens on the host's primary address and delivers each
// request's path and source address on the returned channel.
func startSourceProbe(t *testing.T) (string, <-chan [2]string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	seen := make(chan [2]string, 64)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		select {
		case seen <- [2]string{strings.TrimPrefix(r.URL.Path, "/"), host}:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	route, err := exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	require.NoError(t, err, "%s", route)
	fields := strings.Fields(string(route))
	host := ""
	for i, f := range fields {
		if f == "src" && i+1 < len(fields) {
			host = fields[i+1]
		}
	}
	require.NotEmpty(t, host, "no source address in %q", route)
	return "http://" + net.JoinHostPort(host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)), seen
}
