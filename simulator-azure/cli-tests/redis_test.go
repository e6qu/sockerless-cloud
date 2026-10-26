package azure_cli_test

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// redisCLIResolve resolves a cache's hostName through the simulator's DNS
// front, the resolver a deployment points its clients at.
func redisCLIResolve(t *testing.T, hostName string) string {
	t.Helper()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		return dialer.DialContext(ctx, network, simAzureDNSAddr)
	}}
	lookupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	addresses, err := resolver.LookupHost(lookupCtx, hostName)
	require.NoError(t, err)
	require.NotEmpty(t, addresses)
	return addresses[0]
}

func TestRedisCLI_ARMResources(t *testing.T) {
	t.Parallel()
	name := "cliredis"

	cacheURL := armURL("Microsoft.Cache", "Redis/"+name, "2024-11-01")
	out := azRestLongRunning(t, "PUT", cacheURL, `{
		"location":"eastus",
		"properties":{"sku":{"name":"Basic","family":"C","capacity":1},"minimumTlsVersion":"1.2","redisVersion":"6"},
		"tags":{"env":"cli"}
	}`)
	t.Cleanup(func() {
		_ = azRest("DELETE", cacheURL, "").Run()
	})

	var cache struct {
		Name       string `json:"name"`
		Properties struct {
			SKU               map[string]any `json:"sku"`
			ProvisioningState string         `json:"provisioningState"`
			HostName          string         `json:"hostName"`
			SSLPort           int            `json:"sslPort"`
		} `json:"properties"`
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &cache)
	require.Equal(t, name, cache.Name)
	require.Equal(t, "Basic", cache.Properties.SKU["name"])
	require.Equal(t, "Creating", cache.Properties.ProvisioningState)
	require.Contains(t, cache.Properties.HostName, name+".redis.cache.")
	require.Equal(t, 6380, cache.Properties.SSLPort)
	require.Equal(t, "cli", cache.Tags["env"])

	out = runCLI(t, azRest("GET", cacheURL, ""))
	parseJSON(t, out, &cache)
	require.Equal(t, "Succeeded", cache.Properties.ProvisioningState)

	keysOut := runCLI(t, azRest("POST", armURL("Microsoft.Cache", "Redis/"+name+"/listKeys", "2024-11-01"), ""))
	var keys struct {
		PrimaryKey   string `json:"primaryKey"`
		SecondaryKey string `json:"secondaryKey"`
	}
	parseJSON(t, keysOut, &keys)
	require.NotEmpty(t, keys.PrimaryKey)
	require.NotEqual(t, keys.PrimaryKey, keys.SecondaryKey)

	// The cache answers at its hostName's sslPort to its primary key. Azure
	// presents a certificate a public root signs; the client checks
	// encryption, not the chain.
	address := redisCLIResolve(t, cache.Properties.HostName)
	client := goredis.NewClient(&goredis.Options{
		Addr:      net.JoinHostPort(address, strconv.Itoa(cache.Properties.SSLPort)),
		Password:  keys.PrimaryKey,
		TLSConfig: &tls.Config{ServerName: cache.Properties.HostName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // see above
	})
	t.Cleanup(func() { client.Close() })
	require.NoError(t, client.Set(t.Context(), "cli-greeting", "from az", 0).Err())
	value, err := client.Get(t.Context(), "cli-greeting").Result()
	require.NoError(t, err)
	require.Equal(t, "from az", value)

	ruleURL := armURL("Microsoft.Cache", "Redis/"+name+"/firewallRules/allow-ci", "2024-11-01")
	ruleOut := runCLI(t, azRest("PUT", ruleURL, `{
		"properties":{"startIP":"203.0.113.10","endIP":"203.0.113.10"}
	}`))
	var rule struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			StartIP string `json:"startIP"`
			EndIP   string `json:"endIP"`
		} `json:"properties"`
	}
	parseJSON(t, ruleOut, &rule)
	require.Equal(t, "allow-ci", rule.Name)
	require.Contains(t, rule.ID, "/Redis/"+name+"/firewallRules/allow-ci")
	require.Equal(t, "203.0.113.10", rule.Properties.StartIP)

	listOut := runCLI(t, azRest("GET", armURL("Microsoft.Cache", "Redis/"+name+"/firewallRules", "2024-11-01"), ""))
	require.Contains(t, listOut, `"name": "allow-ci"`)

	runCLI(t, azRest("DELETE", ruleURL, ""))
}
