package main

import (
	"net/url"
	"testing"
)

func TestElastiCacheConditionKeysReadTheRequest(t *testing.T) {
	cases := []struct {
		operation string
		form      url.Values
		want      map[string][]string
	}{
		{"ModifyGlobalReplicationGroup", url.Values{
			"GlobalReplicationGroupId": {"g"},
			"AutomaticFailoverEnabled": {"true"},
			"CacheNodeType":            {"cache.r6g.large"},
			"EngineVersion":            {"7.1"},
		}, map[string][]string{
			"elasticache:AutomaticFailoverEnabled": {"true"},
			"elasticache:CacheNodeType":            {"cache.r6g.large"},
			"elasticache:EngineVersion":            {"7.1"},
		}},
		{"CreateCacheParameterGroup", url.Values{"CacheParameterGroupName": {"pg"}, "CacheParameterGroupFamily": {"redis7"}},
			map[string][]string{"elasticache:CacheParameterGroupName": {"pg"}}},
		{"ResetCacheParameterGroup", url.Values{"CacheParameterGroupName": {"pg"}},
			map[string][]string{"elasticache:CacheParameterGroupName": {"pg"}}},
		{"CopySnapshot", url.Values{"SourceSnapshotName": {"a"}, "TargetSnapshotName": {"b"}, "KmsKeyId": {"alias/cache"}},
			map[string][]string{"elasticache:KmsKeyId": {"alias/cache"}}},
		{"ModifyReplicationGroupShardConfiguration", url.Values{"ReplicationGroupId": {"rg"}, "NodeGroupCount": {"3"}},
			map[string][]string{"elasticache:NumNodeGroups": {"3"}}},
		{"IncreaseNodeGroupsInGlobalReplicationGroup", url.Values{"GlobalReplicationGroupId": {"g"}, "NodeGroupCount": {"4"}},
			map[string][]string{"elasticache:NumNodeGroups": {"4"}}},
		{"DecreaseReplicaCount", url.Values{"ReplicationGroupId": {"rg"}, "NewReplicaCount": {"1"}},
			map[string][]string{"elasticache:ReplicasPerNodeGroup": {"1"}}},
		{"CreateUser", url.Values{"UserId": {"u"}, "AuthenticationMode.Type": {"iam"}},
			map[string][]string{"elasticache:UserAuthenticationMode": {"iam"}}},
		{"ModifyUser", url.Values{"UserId": {"u"}, "AuthenticationMode.Type": {"password"}},
			map[string][]string{"elasticache:UserAuthenticationMode": {"password"}}},
	}
	for _, c := range cases {
		t.Run(c.operation, func(t *testing.T) {
			ctx := queryServiceConditionContext(t, "elasticache", c.operation, c.form)
			assertServiceConditionContext(t, ctx, c.want)
		})
	}
}

func TestElastiCacheConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	ctx := queryServiceConditionContext(t, "elasticache", "ModifyGlobalReplicationGroup", url.Values{
		"GlobalReplicationGroupId": {"g"},
		"ApplyImmediately":         {"true"},
	})
	if len(ctx) != 0 {
		t.Errorf("a modification naming nothing settled %v", ctx)
	}
	// CreateReplicationGroup declares none of these keys.
	ctx = queryServiceConditionContext(t, "elasticache", "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":      {"rg"},
		"CacheNodeType":           {"cache.r6g.large"},
		"CacheParameterGroupName": {"pg"},
	})
	if len(ctx) != 0 {
		t.Errorf("CreateReplicationGroup settled %v", ctx)
	}
}
