package azure_tf_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestTerraformRedisCacheServesItsHostName connects to the hostname, ports and
// access keys Terraform read back: the hostname resolves through the
// simulator's DNS, the engine answers over TLS and plaintext to either key, and
// a write through one connection reads back through the other.
func TestTerraformRedisCacheServesItsHostName(t *testing.T) {
	t.Parallel()
	dir := tfWorkspaceFrom(t, mustAbs("redis"))
	run := func(name string, args ...string) {
		t.Helper()
		out, err := runTimed(t, name, terraformCmd(dir, args...))
		require.NoError(t, err, "%s failed:\n%s", name, out)
	}
	run("terraform init", "init")
	run("terraform apply", "apply", "-auto-approve")
	run("terraform plan", "plan", "-detailed-exitcode")

	outputs := readOutputs(t, dir)
	hostname := outputs.must(t, "hostname")
	require.Contains(t, hostname, "tfazrmredisengine.redis.cache.")
	require.Equal(t, "6380", outputs.must(t, "ssl_port"))
	require.Equal(t, "6379", outputs.must(t, "port"))
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		return dialer.DialContext(ctx, network, simAzureDNSAddr)
	}}
	lookupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	addresses, err := resolver.LookupHost(lookupCtx, hostname)
	require.NoError(t, err)
	require.NotEmpty(t, addresses)

	// Azure presents a certificate a public root signs for the hostname; the
	// client checks encryption, not the chain.
	secured := goredis.NewClient(&goredis.Options{
		Addr: net.JoinHostPort(addresses[0], outputs.must(t, "ssl_port")), Password: outputs.must(t, "primary_access_key"),
		TLSConfig: &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // see above
	})
	t.Cleanup(func() { secured.Close() })
	require.NoError(t, secured.Set(t.Context(), "terraform", "applied", 0).Err())
	plaintext := goredis.NewClient(&goredis.Options{
		Addr: net.JoinHostPort(addresses[0], outputs.must(t, "port")), Password: outputs.must(t, "secondary_access_key"),
	})
	t.Cleanup(func() { plaintext.Close() })
	value, err := plaintext.Get(t.Context(), "terraform").Result()
	require.NoError(t, err)
	require.Equal(t, "applied", value)
	policy, err := plaintext.ConfigGet(t.Context(), "maxmemory-policy").Result()
	require.NoError(t, err)
	require.Equal(t, "allkeys-lru", policy["maxmemory-policy"], "redis_configuration reaches the engine")

	run("terraform destroy", "destroy", "-auto-approve")
}
