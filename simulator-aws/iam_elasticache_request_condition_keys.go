package main

import (
	"net/http"
	"strconv"
)

// Amazon ElastiCache's request condition keys: the node types, versions,
// shard and replica counts and authentication a request asks for, which a
// policy holds to an approved set.

func init() {
	registerIAMRequestConditionPopulator("elasticache", iamPopulateElastiCacheRequestConditionKeys)
}

func iamPopulateElastiCacheRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	switch operation {
	case "ModifyGlobalReplicationGroup":
		ecSetBool(ctx, "elasticache:AutomaticFailoverEnabled", r.FormValue("AutomaticFailoverEnabled"))
		ecSetString(ctx, "elasticache:CacheNodeType", r.FormValue("CacheNodeType"))
		ecSetString(ctx, "elasticache:EngineVersion", r.FormValue("EngineVersion"))
	case "CreateCacheParameterGroup", "DeleteCacheParameterGroup", "ModifyCacheParameterGroup", "ResetCacheParameterGroup":
		ecSetString(ctx, "elasticache:CacheParameterGroupName", r.FormValue("CacheParameterGroupName"))
	case "CopySnapshot":
		ecSetString(ctx, "elasticache:KmsKeyId", r.FormValue("KmsKeyId"))
	case "DecreaseNodeGroupsInGlobalReplicationGroup", "IncreaseNodeGroupsInGlobalReplicationGroup", "ModifyReplicationGroupShardConfiguration":
		ecSetInteger(ctx, "elasticache:NumNodeGroups", r.FormValue("NodeGroupCount"))
	case "DecreaseReplicaCount", "IncreaseReplicaCount":
		ecSetInteger(ctx, "elasticache:ReplicasPerNodeGroup", r.FormValue("NewReplicaCount"))
	case "CreateUser", "ModifyUser":
		ecSetString(ctx, "elasticache:UserAuthenticationMode", r.FormValue("AuthenticationMode.Type"))
	}
}

func ecSetString(ctx map[string][]string, key, value string) {
	if value != "" {
		ctx[key] = []string{value}
	}
}

func ecSetBool(ctx map[string][]string, key, value string) {
	if parsed, err := strconv.ParseBool(value); err == nil {
		ctx[key] = []string{strconv.FormatBool(parsed)}
	}
}

func ecSetInteger(ctx map[string][]string, key, value string) {
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		ctx[key] = []string{strconv.FormatInt(parsed, 10)}
	}
}
