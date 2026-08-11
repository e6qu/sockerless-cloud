package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apigateway "google.golang.org/api/apigateway/v1"
	"google.golang.org/api/option"
	redis "google.golang.org/api/redis/v1"
)

func redisService(t *testing.T) *redis.Service {
	t.Helper()
	svc, err := redis.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func apigatewayService(t *testing.T) *apigateway.Service {
	t.Helper()
	svc, err := apigateway.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestMemorystoreRedis_InstanceLifecycle exercises the Memorystore
// Redis instance lifecycle: create (LRO) → get → list → patch (LRO)
// → delete (LRO).
func TestMemorystoreRedis_InstanceLifecycle(t *testing.T) {
	svc := redisService(t)
	parent := "projects/test-project/locations/us-central1"
	id := "test-cache"

	op, err := svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		Tier:         "BASIC",
		MemorySizeGb: 1,
	}).InstanceId(id).Do()
	require.NoError(t, err)
	assert.True(t, op.Done, "sim emits synchronous done=true LROs")

	inst, err := svc.Projects.Locations.Instances.Get(parent + "/instances/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, parent+"/instances/"+id, inst.Name)
	assert.Equal(t, "READY", inst.State)
	assert.Equal(t, int64(6379), inst.Port, "redis default port")

	list, err := svc.Projects.Locations.Instances.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, x := range list.Instances {
		if x.Name == inst.Name {
			found = true
			break
		}
	}
	assert.True(t, found)

	// Patch.
	_, err = svc.Projects.Locations.Instances.Patch(inst.Name, &redis.Instance{
		MemorySizeGb: 5,
	}).UpdateMask("memorySizeGb").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Locations.Instances.Get(inst.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(5), got.MemorySizeGb)

	delOp, err := svc.Projects.Locations.Instances.Delete(inst.Name).Do()
	require.NoError(t, err)
	assert.True(t, delOp.Done)
}

// TestMemorystoreRedis_ClusterLifecycle exercises the Redis Cluster
// surface: create (LRO) → get → list → patch (LRO) → certificate
// authority read → delete (LRO).
func TestMemorystoreRedis_ClusterLifecycle(t *testing.T) {
	svc := redisService(t)
	parent := "projects/test-project/locations/us-central1"
	id := "test-cluster"

	op, err := svc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{
		ShardCount:   3,
		ReplicaCount: 1,
		NodeType:     "REDIS_HIGHMEM_MEDIUM",
	}).ClusterId(id).Do()
	require.NoError(t, err)
	assert.True(t, op.Done, "sim emits synchronous done=true LROs")

	name := parent + "/clusters/" + id
	cl, err := svc.Projects.Locations.Clusters.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, cl.Name)
	assert.Equal(t, "ACTIVE", cl.State)
	assert.Equal(t, int64(3), cl.ShardCount)
	require.NotEmpty(t, cl.DiscoveryEndpoints)
	assert.Equal(t, int64(6379), cl.DiscoveryEndpoints[0].Port)

	list, err := svc.Projects.Locations.Clusters.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, x := range list.Clusters {
		if x.Name == name {
			found = true
		}
	}
	assert.True(t, found)

	_, err = svc.Projects.Locations.Clusters.Patch(name, &redis.Cluster{
		ReplicaCount: 2,
	}).UpdateMask("replicaCount").Do()
	require.NoError(t, err)
	got, err := svc.Projects.Locations.Clusters.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.ReplicaCount)

	ca, err := svc.Projects.Locations.Clusters.GetCertificateAuthority(name + "/certificateAuthority").Do()
	require.NoError(t, err)
	require.NotNil(t, ca.ManagedServerCa)
	require.NotEmpty(t, ca.ManagedServerCa.CaCerts)
	assert.NotEmpty(t, ca.ManagedServerCa.CaCerts[0].Certificates)

	delOp, err := svc.Projects.Locations.Clusters.Delete(name).Do()
	require.NoError(t, err)
	assert.True(t, delOp.Done)
}

// TestMemorystoreRedis_BackupCollections exercises a cluster :backup that
// materializes a BackupCollection + Backup, then reads them back through the
// backupCollections / backups list+get APIs and deletes the backup.
func TestMemorystoreRedis_BackupCollections(t *testing.T) {
	svc := redisService(t)
	parent := "projects/test-project/locations/us-east1"
	id := "backup-cluster"

	_, err := svc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{
		ShardCount: 1,
	}).ClusterId(id).Do()
	require.NoError(t, err)

	name := parent + "/clusters/" + id
	bop, err := svc.Projects.Locations.Clusters.Backup(name, &redis.BackupClusterRequest{
		BackupId: "snap-1",
	}).Do()
	require.NoError(t, err)
	assert.True(t, bop.Done)

	// The backup collection is named after the cluster.
	bcName := parent + "/backupCollections/" + id
	bc, err := svc.Projects.Locations.BackupCollections.Get(bcName).Do()
	require.NoError(t, err)
	assert.Equal(t, bcName, bc.Name)
	assert.Equal(t, name, bc.Cluster)

	bcList, err := svc.Projects.Locations.BackupCollections.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, bcList.BackupCollections)

	backups, err := svc.Projects.Locations.BackupCollections.Backups.List(bcName).Do()
	require.NoError(t, err)
	require.Len(t, backups.Backups, 1)
	backupName := backups.Backups[0].Name
	assert.Equal(t, "ON_DEMAND", backups.Backups[0].BackupType)

	got, err := svc.Projects.Locations.BackupCollections.Backups.Get(backupName).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State)

	expOp, err := svc.Projects.Locations.BackupCollections.Backups.Export(backupName, &redis.ExportBackupRequest{
		GcsBucket: "gs://sim-backups",
	}).Do()
	require.NoError(t, err)
	assert.True(t, expOp.Done)

	delOp, err := svc.Projects.Locations.BackupCollections.Backups.Delete(backupName).Do()
	require.NoError(t, err)
	assert.True(t, delOp.Done)
}

// TestMemorystoreRedis_AclPolicies exercises ACL policy CRUD plus the
// token-auth-user sub-resource and its auth tokens.
func TestMemorystoreRedis_AclPolicies(t *testing.T) {
	svc := redisService(t)
	parent := "projects/test-project/locations/us-west1"

	// ACL policy create returns the resource directly (not an LRO).
	pol, err := svc.Projects.Locations.AclPolicies.Create(parent, &redis.AclPolicy{
		Rules: []*redis.AclRule{{Username: "app", Rule: "+@all ~*"}},
	}).AclPolicyId("policy-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", pol.State)
	require.Len(t, pol.Rules, 1)
	assert.Equal(t, "app", pol.Rules[0].Username)

	polName := parent + "/aclPolicies/policy-1"
	got, err := svc.Projects.Locations.AclPolicies.Get(polName).Do()
	require.NoError(t, err)
	assert.Equal(t, polName, got.Name)

	pList, err := svc.Projects.Locations.AclPolicies.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, pList.AclPolicies)

	_, err = svc.Projects.Locations.AclPolicies.Patch(polName, &redis.AclPolicy{
		Rules: []*redis.AclRule{{Username: "app", Rule: "+get ~k*"}},
	}).UpdateMask("rules").Do()
	require.NoError(t, err)
	got, err = svc.Projects.Locations.AclPolicies.Get(polName).Do()
	require.NoError(t, err)
	assert.Equal(t, "+get ~k*", got.Rules[0].Rule)

	delOp, err := svc.Projects.Locations.AclPolicies.Delete(polName).Do()
	require.NoError(t, err)
	assert.True(t, delOp.Done)

	// Token-auth users + auth tokens, nested under a cluster.
	clParent := parent
	_, err = svc.Projects.Locations.Clusters.Create(clParent, &redis.Cluster{
		AuthorizationMode: "AUTH_MODE_IAM_AUTH",
	}).ClusterId("acl-cluster").Do()
	require.NoError(t, err)
	clName := clParent + "/clusters/acl-cluster"

	addOp, err := svc.Projects.Locations.Clusters.AddTokenAuthUser(clName, &redis.AddTokenAuthUserRequest{
		TokenAuthUser: "svc-account",
	}).Do()
	require.NoError(t, err)
	assert.True(t, addOp.Done)

	userName := clName + "/tokenAuthUsers/svc-account"
	uList, err := svc.Projects.Locations.Clusters.TokenAuthUsers.List(clName).Do()
	require.NoError(t, err)
	foundUser := false
	for _, u := range uList.TokenAuthUsers {
		if u.Name == userName {
			foundUser = true
		}
	}
	assert.True(t, foundUser)

	tokOp, err := svc.Projects.Locations.Clusters.TokenAuthUsers.AddAuthToken(userName, &redis.AddAuthTokenRequest{
		AuthToken: &redis.AuthToken{Name: "tok-1"},
	}).Do()
	require.NoError(t, err)
	assert.True(t, tokOp.Done)

	tList, err := svc.Projects.Locations.Clusters.TokenAuthUsers.AuthTokens.List(userName).Do()
	require.NoError(t, err)
	require.Len(t, tList.AuthTokens, 1)
	assert.NotEmpty(t, tList.AuthTokens[0].Token)
}

// TestAPIGateway_Lifecycle exercises Api + ApiConfig + Gateway CRUD.
func TestAPIGateway_Lifecycle(t *testing.T) {
	svc := apigatewayService(t)
	project := "test-project"
	parentGlobal := "projects/" + project + "/locations/global"
	apiId := "test-api"

	// Create Api.
	apiOp, err := svc.Projects.Locations.Apis.Create(parentGlobal, &apigateway.ApigatewayApi{
		DisplayName: "Test API",
	}).ApiId(apiId).Do()
	require.NoError(t, err)
	assert.True(t, apiOp.Done)

	// Get Api.
	api, err := svc.Projects.Locations.Apis.Get(parentGlobal + "/apis/" + apiId).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", api.State)

	// Create ApiConfig.
	cfgOp, err := svc.Projects.Locations.Apis.Configs.Create(api.Name, &apigateway.ApigatewayApiConfig{
		DisplayName: "v1",
	}).ApiConfigId("v1").Do()
	require.NoError(t, err)
	assert.True(t, cfgOp.Done)

	// List ApiConfigs.
	cfgList, err := svc.Projects.Locations.Apis.Configs.List(api.Name).Do()
	require.NoError(t, err)
	require.Len(t, cfgList.ApiConfigs, 1)

	// Create Gateway in a regional location.
	parentRegion := "projects/" + project + "/locations/us-central1"
	gwOp, err := svc.Projects.Locations.Gateways.Create(parentRegion, &apigateway.ApigatewayGateway{
		ApiConfig: cfgList.ApiConfigs[0].Name,
	}).GatewayId("test-gw").Do()
	require.NoError(t, err)
	assert.True(t, gwOp.Done)

	gw, err := svc.Projects.Locations.Gateways.Get(parentRegion + "/gateways/test-gw").Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", gw.State)
	assert.NotEmpty(t, gw.DefaultHostname)

	// Patch Api (displayName) — LRO, then read back the mutation.
	apiPatchOp, err := svc.Projects.Locations.Apis.Patch(api.Name, &apigateway.ApigatewayApi{
		DisplayName: "Test API renamed",
	}).UpdateMask("displayName").Do()
	require.NoError(t, err)
	assert.True(t, apiPatchOp.Done)
	api, err = svc.Projects.Locations.Apis.Get(api.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Test API renamed", api.DisplayName)

	// Patch ApiConfig (displayName).
	cfgName := cfgList.ApiConfigs[0].Name
	cfgPatchOp, err := svc.Projects.Locations.Apis.Configs.Patch(cfgName, &apigateway.ApigatewayApiConfig{
		DisplayName: "v1 renamed",
	}).UpdateMask("displayName").Do()
	require.NoError(t, err)
	assert.True(t, cfgPatchOp.Done)
	cfg, err := svc.Projects.Locations.Apis.Configs.Get(cfgName).Do()
	require.NoError(t, err)
	assert.Equal(t, "v1 renamed", cfg.DisplayName)

	// Patch Gateway (displayName).
	gwPatchOp, err := svc.Projects.Locations.Gateways.Patch(gw.Name, &apigateway.ApigatewayGateway{
		DisplayName: "Gateway renamed",
	}).UpdateMask("displayName").Do()
	require.NoError(t, err)
	assert.True(t, gwPatchOp.Done)
	gw, err = svc.Projects.Locations.Gateways.Get(gw.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Gateway renamed", gw.DisplayName)

	// Cleanup: delete in dependency order.
	_, _ = svc.Projects.Locations.Gateways.Delete(gw.Name).Do()
	_, _ = svc.Projects.Locations.Apis.Configs.Delete(cfgList.ApiConfigs[0].Name).Do()
	_, _ = svc.Projects.Locations.Apis.Delete(api.Name).Do()
}
