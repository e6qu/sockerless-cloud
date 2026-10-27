package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisLookupHost resolves a cache's hostName through the simulator's DNS.
func redisLookupHost(t *testing.T, hostName string) string {
	t.Helper()
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addresses, err := azureDNSResolver().LookupHost(lookupCtx, hostName)
	require.NoError(t, err, "resolve %s", hostName)
	require.NotEmpty(t, addresses)
	return addresses[0]
}

func redisCacheClient(t *testing.T, address string, port int32, password string, secured *tls.Config) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr: net.JoinHostPort(address, strconv.Itoa(int(port))), Password: password, TLSConfig: secured,
		MaxRetries: -1, DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { client.Close() })
	return client
}

// A Standard cache answers at its hostName: over TLS on sslPort and, while
// enableNonSslPort holds, in plaintext on port, to either access key; a
// regenerated key replaces the old one, and a flush empties the cache.
func TestRedis_CacheServesItsHostName(t *testing.T) {
	t.Parallel()
	rg := "redis-engine-rg"
	ensureRedisResourceGroup(t, rg)
	client, err := armredis.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const name = "redis-engine"
	poller, err := client.BeginCreate(ctx, rg, name, armredis.CreateParameters{
		Location: ptrStr("eastus"),
		Properties: &armredis.CreateProperties{
			SKU:               &armredis.SKU{Name: ptr(armredis.SKUNameStandard), Family: ptr(armredis.SKUFamilyC), Capacity: ptr[int32](1)},
			EnableNonSSLPort:  ptr(true),
			MinimumTLSVersion: ptr(armredis.TLSVersionOne2),
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if deletion, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = deletion.PollUntilDone(ctx, nil)
		}
	})
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	properties := created.Properties
	require.NotNil(t, properties)
	assert.Equal(t, armredis.ProvisioningStateSucceeded, *properties.ProvisioningState)
	assert.Equal(t, int32(6380), *properties.SSLPort)
	assert.Equal(t, int32(6379), *properties.Port)
	hostName := *properties.HostName
	address := redisLookupHost(t, hostName)
	assert.True(t, net.ParseIP(address).IsLoopback(), "%s resolves to %s", hostName, address)

	keys, err := client.ListKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	// Azure presents a certificate a public root signs for the hostName; the
	// client checks encryption, not the chain.
	secured := &tls.Config{ServerName: hostName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // see above
	primary := redisCacheClient(t, address, *properties.SSLPort, *keys.PrimaryKey, secured)
	require.NoError(t, primary.Set(ctx, "greeting", "hello from azure", 0).Err())
	acknowledged, err := primary.Wait(ctx, 1, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), acknowledged, "the Standard tier's replica acknowledged the write")
	secondary := redisCacheClient(t, address, *properties.SSLPort, *keys.SecondaryKey, secured)
	value, err := secondary.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello from azure", value)
	plaintext := redisCacheClient(t, address, *properties.Port, *keys.PrimaryKey, nil)
	value, err = plaintext.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello from azure", value)
	tls10 := &tls.Config{ServerName: hostName, MaxVersion: tls.VersionTLS11, MinVersion: tls.VersionTLS10, InsecureSkipVerify: true} //nolint:gosec // see above
	assert.Error(t, redisCacheClient(t, address, *properties.SSLPort, *keys.PrimaryKey, tls10).Ping(ctx).Err(),
		"minimumTlsVersion 1.2 refuses an older handshake")

	regenerated, err := client.RegenerateKey(ctx, rg, name, armredis.RegenerateKeyParameters{KeyType: ptr(armredis.RedisKeyTypePrimary)}, nil)
	require.NoError(t, err)
	assert.Error(t, redisCacheClient(t, address, *properties.SSLPort, *keys.PrimaryKey, secured).Ping(ctx).Err(),
		"the regenerated primary key no longer authenticates")
	rotated := redisCacheClient(t, address, *properties.SSLPort, *regenerated.PrimaryKey, secured)
	require.NoError(t, rotated.Ping(ctx).Err())
	require.NoError(t, redisCacheClient(t, address, *properties.SSLPort, *regenerated.SecondaryKey, secured).Ping(ctx).Err())

	updated, err := client.BeginUpdate(ctx, rg, name, armredis.UpdateParameters{
		Properties: &armredis.UpdateProperties{EnableNonSSLPort: ptr(false)},
	}, nil)
	require.NoError(t, err)
	_, err = updated.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Error(t, redisCacheClient(t, address, *properties.Port, *regenerated.PrimaryKey, nil).Ping(ctx).Err(),
		"the non-SSL port closed")

	flush, err := client.BeginFlushCache(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = flush.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, goredis.Nil, rotated.Get(ctx, "greeting").Err(), "the flush emptied the cache")
}
