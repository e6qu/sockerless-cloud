package azure_cli_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/freeports"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `az redis export` writes a Premium cache's keys into a blob container the
// CLI's own SAS addresses, and `az redis import` loads the blob into another
// cache.
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Cache/Redis/{name}/export
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Cache/Redis/{name}/import
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/operationResults/{opId}

type redisTransferCLICache struct {
	Name     string `json:"name"`
	HostName string `json:"hostName"`
	SSLPort  int    `json:"sslPort"`
}

// redisTransferCLIClient connects to a cache at its hostName, resolved through
// the simulator's DNS front.
func redisTransferCLIClient(t *testing.T, env azLoginEnv, dnsAddr, rg, name string) *goredis.Client {
	t.Helper()
	var cache redisTransferCLICache
	parseJSON(t, runCLI(t, env.command("redis", "show", "-n", name, "-g", rg, "-o", "json")), &cache)
	var keys struct {
		PrimaryKey string `json:"primaryKey"`
	}
	parseJSON(t, runCLI(t, env.command("redis", "list-keys", "-n", name, "-g", rg, "-o", "json")), &keys)
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		return dialer.DialContext(ctx, network, dnsAddr)
	}}
	lookupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	addresses, err := resolver.LookupHost(lookupCtx, cache.HostName)
	require.NoError(t, err)
	require.NotEmpty(t, addresses)
	client := goredis.NewClient(&goredis.Options{
		Addr:     net.JoinHostPort(addresses[0], strconv.Itoa(cache.SSLPort)),
		Password: keys.PrimaryKey,
		// Azure presents a certificate a public root signs; the client checks
		// encryption, not the chain.
		TLSConfig:  &tls.Config{ServerName: cache.HostName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // see above
		MaxRetries: -1, DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { client.Close() })
	return client
}

func TestRedisCLI_ExportAndImport(t *testing.T) {
	t.Parallel()
	dnsPort, err := freeports.TCPUDP()
	require.NoError(t, err)
	dnsAddr := fmt.Sprintf("127.0.0.1:%d", dnsPort)
	env := startAzTLSSimulator(t, "SIM_AZURE_DNS_LISTEN_ADDR="+dnsAddr)
	tagCLILogin(t, env, "sockerless-redis-transfer")

	const rg, account, container = "cli-redis-transfer-rg", "cliredistransfer", "snapshots"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-n", account, "-g", rg,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json"))
	var accountKeys []struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list",
		"-g", rg, "-n", account, "-o", "json")), &accountKeys)
	require.NotEmpty(t, accountKeys)
	accountKey := accountKeys[0].Value
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "container", "create", "--name", container)...))
	sasToken := strings.Trim(strings.TrimSpace(runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "container", "generate-sas", "--name", container, "--permissions", "racwdlmt",
		"--expiry", time.Now().UTC().Add(24*time.Hour).Format("2006-01-02T15:04Z"))...))), `"`)
	require.Contains(t, sasToken, "sig=")
	containerURL := strings.TrimSuffix(strings.Replace(env.baseURL, "https://localhost", "https://"+account+".blob.localhost", 1), "/") +
		"/" + container

	for _, cache := range []string{"cliredisexport", "cliredisimport"} {
		runCLI(t, env.command("redis", "create", "-n", cache, "-g", rg, "-l", "eastus",
			"--sku", "Premium", "--vm-size", "p1", "-o", "json"))
	}
	source := redisTransferCLIClient(t, env, dnsAddr, rg, "cliredisexport")
	require.NoError(t, source.Set(t.Context(), "greeting", "exported by az", 0).Err())

	runCLI(t, env.command("redis", "export", "-n", "cliredisexport", "-g", rg,
		"--container", containerURL+"?"+sasToken, "--prefix", "cli-snapshot", "--file-format", "rdb"))
	var shown struct {
		Properties struct {
			BlobType string `json:"blobType"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "blob", "show", "--container-name", container, "--name", "cli-snapshot")...)), &shown)
	assert.Equal(t, "PageBlob", shown.Properties.BlobType, "Premium exports an RDB page blob")

	target := redisTransferCLIClient(t, env, dnsAddr, rg, "cliredisimport")
	require.NoError(t, target.Set(t.Context(), "preexisting", "dropped by the import", 0).Err())
	runCLI(t, env.command("redis", "import", "-n", "cliredisimport", "-g", rg,
		"--files", containerURL+"/cli-snapshot?"+sasToken, "--file-format", "rdb", "--yes"))
	target = redisTransferCLIClient(t, env, dnsAddr, rg, "cliredisimport")
	assert.Equal(t, "exported by az", target.Get(t.Context(), "greeting").Val())
	assert.Zero(t, target.Exists(t.Context(), "preexisting").Val(), "importing deletes the cache's existing data")

	failure := runStorageCLIExpectFailure(t, env.command("redis", "export", "-n", "cliredisexport", "-g", rg,
		"--container", "https://example.blob.core.windows.net/backups?"+sasToken, "--prefix", "unreachable"))
	assert.Contains(t, failure, "cannot be reached")
}
