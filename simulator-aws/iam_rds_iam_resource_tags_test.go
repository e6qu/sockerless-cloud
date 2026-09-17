package main

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Amazon RDS and AWS Identity and Access Management do not spell their
// resource-tag condition keys <service>:ResourceTag/<k>. These tests hold the
// gate to the spellings the vendored service references declare, and — just as
// importantly — to NOT writing the spelling of a resource type the request did
// not target, because a policy written for a DB cluster must not be satisfied
// by the tags of a DB instance.

// assertExactConditionContext asserts ctx is exactly want, so a key the gate
// should not have written fails the test instead of passing unnoticed.
func assertExactConditionContext(t *testing.T, ctx, want map[string][]string) {
	t.Helper()
	if reflect.DeepEqual(ctx, want) {
		return
	}
	t.Errorf("condition context = %s, want %s", sortedContext(ctx), sortedContext(want))
}

func sortedContext(ctx map[string][]string) string {
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, ctx[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// rdsResourceTagFixtures stores one tagged resource of every RDS type the
// reference declares a tag condition key for and the simulator keeps tags on.
func rdsResourceTagFixtures() {
	rdsInstances.Put("probe-db", RDSInstance{
		DBInstanceIdentifier: "probe-db", ARN: rdsInstanceARN("probe-db"),
		Tags: map[string]string{"owner": "platform"},
	})
	rdsClusters.Put("probe-cluster", RDSCluster{
		DBClusterIdentifier: "probe-cluster", ARN: rdsClusterARN("probe-cluster"),
		Tags: map[string]string{"owner": "ledger"},
	})
	rdsSnapshots.Put("probe-snap", RDSSnapshot{
		DBSnapshotIdentifier: "probe-snap", ARN: rdsSnapshotARN("probe-snap"),
		Tags: map[string]string{"owner": "backups"},
	})
	rdsClusterSnapshots.Put("probe-cluster-snap", RDSClusterSnapshot{
		DBClusterSnapshotIdentifier: "probe-cluster-snap", ARN: rdsClusterSnapshotARN("probe-cluster-snap"),
		Tags: map[string]string{"owner": "cluster-backups"},
	})
	rdsParamGroups.Put("probe-pg", RDSParamGroup{
		DBParameterGroupName: "probe-pg", ARN: rdsParamGroupARN("probe-pg"),
		Tags: map[string]string{"owner": "tuning"},
	})
	rdsClusterParamGroups.Put("probe-cluster-pg", RDSClusterParamGroup{
		DBClusterParameterGroupName: "probe-cluster-pg", ARN: rdsClusterParamGroupARN("probe-cluster-pg"),
		Tags: map[string]string{"owner": "cluster-tuning"},
	})
	rdsOptionGroups.Put("probe-og", RDSOptionGroup{
		OptionGroupName: "probe-og", ARN: rdsOptionGroupARN("probe-og"),
		Tags: map[string]string{"owner": "options"},
	})
	rdsSubnetGroups.Put("probe-subgrp", RDSSubnetGroup{
		DBSubnetGroupName: "probe-subgrp", ARN: rdsSubnetGroupARN("probe-subgrp"),
		Tags: map[string]string{"owner": "networking"},
	})
	rdsDBSecurityGroups.Put("probe-secgrp", RDSDBSecurityGroup{
		DBSecurityGroupName: "probe-secgrp", ARN: rdsSecurityGroupARN("probe-secgrp"),
		Tags: map[string]string{"owner": "security"},
	})
	rdsEventSubscriptions.Put("probe-es", RDSEventSubscription{
		SubscriptionName: "probe-es", ARN: rdsEventSubscriptionARN("probe-es"),
		Tags: map[string]string{"owner": "events"},
	})
}

// TestIAMRDSResourceTagsUseTheSpellingsRDSDeclares walks every RDS resource
// type the simulator tags, by identifier and by ARN, and asserts the gate wrote
// aws:ResourceTag/<k> plus that type's own key — and nothing else. Before this,
// the gate wrote rds:ResourceTag/<k>, a key the reference does not declare at
// all, so every documented RDS tag condition went unmatched.
func TestIAMRDSResourceTagsUseTheSpellingsRDSDeclares(t *testing.T) {
	buildConformanceSimulator(t)
	rdsResourceTagFixtures()

	cases := []struct {
		name string
		form map[string]string
		want map[string][]string
	}{
		{"a DB instance named by identifier",
			map[string]string{"Action": "ModifyDBInstance", "DBInstanceIdentifier": "probe-db"},
			map[string][]string{"aws:ResourceTag/owner": {"platform"}, "rds:db-tag/owner": {"platform"}}},
		{"a DB instance named by ARN",
			map[string]string{"Action": "ListTagsForResource", "ResourceName": rdsInstanceARN("probe-db")},
			map[string][]string{"aws:ResourceTag/owner": {"platform"}, "rds:db-tag/owner": {"platform"}}},
		{"a DB cluster named by identifier",
			map[string]string{"Action": "ModifyDBCluster", "DBClusterIdentifier": "probe-cluster"},
			map[string][]string{"aws:ResourceTag/owner": {"ledger"}, "rds:cluster-tag/owner": {"ledger"}}},
		{"a DB cluster named by ARN",
			map[string]string{"Action": "ListTagsForResource", "ResourceName": rdsClusterARN("probe-cluster")},
			map[string][]string{"aws:ResourceTag/owner": {"ledger"}, "rds:cluster-tag/owner": {"ledger"}}},
		{"a DB snapshot",
			map[string]string{"Action": "DeleteDBSnapshot", "DBSnapshotIdentifier": "probe-snap"},
			map[string][]string{"aws:ResourceTag/owner": {"backups"}, "rds:snapshot-tag/owner": {"backups"}}},
		{"a DB cluster snapshot",
			map[string]string{"Action": "DeleteDBClusterSnapshot", "DBClusterSnapshotIdentifier": "probe-cluster-snap"},
			map[string][]string{"aws:ResourceTag/owner": {"cluster-backups"}, "rds:cluster-snapshot-tag/owner": {"cluster-backups"}}},
		{"a DB parameter group",
			map[string]string{"Action": "ModifyDBParameterGroup", "DBParameterGroupName": "probe-pg"},
			map[string][]string{"aws:ResourceTag/owner": {"tuning"}, "rds:pg-tag/owner": {"tuning"}}},
		{"a DB cluster parameter group",
			map[string]string{"Action": "ModifyDBClusterParameterGroup", "DBClusterParameterGroupName": "probe-cluster-pg"},
			map[string][]string{"aws:ResourceTag/owner": {"cluster-tuning"}, "rds:cluster-pg-tag/owner": {"cluster-tuning"}}},
		{"an option group",
			map[string]string{"Action": "ModifyOptionGroup", "OptionGroupName": "probe-og"},
			map[string][]string{"aws:ResourceTag/owner": {"options"}, "rds:og-tag/owner": {"options"}}},
		{"a DB subnet group",
			map[string]string{"Action": "ModifyDBSubnetGroup", "DBSubnetGroupName": "probe-subgrp"},
			map[string][]string{"aws:ResourceTag/owner": {"networking"}, "rds:subgrp-tag/owner": {"networking"}}},
		{"a DB security group",
			map[string]string{"Action": "DeleteDBSecurityGroup", "DBSecurityGroupName": "probe-secgrp"},
			map[string][]string{"aws:ResourceTag/owner": {"security"}, "rds:secgrp-tag/owner": {"security"}}},
		{"an event subscription",
			map[string]string{"Action": "ModifyEventSubscription", "SubscriptionName": "probe-es"},
			map[string][]string{"aws:ResourceTag/owner": {"events"}, "rds:es-tag/owner": {"events"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := map[string][]string{}
			if !iamPopulateServiceResourceTags(formRequest(c.form), "rds", ctx) {
				t.Fatal("the dispatcher did not handle rds")
			}
			assertExactConditionContext(t, ctx, c.want)
		})
	}
}

// TestIAMRDSResourceTagsDoNotCrossResourceTypes is the half that matters for a
// deny: the key of a resource type the request did not target must be absent,
// so a policy scoped to DB clusters cannot be satisfied by a DB instance's
// tags, and vice versa.
func TestIAMRDSResourceTagsDoNotCrossResourceTypes(t *testing.T) {
	buildConformanceSimulator(t)
	rdsResourceTagFixtures()

	instance := map[string][]string{}
	iamPopulateServiceResourceTags(formRequest(map[string]string{
		"Action": "ModifyDBInstance", "DBInstanceIdentifier": "probe-db",
	}), "rds", instance)
	for _, absent := range []string{
		"rds:ResourceTag/owner", "rds:cluster-tag/owner", "rds:pg-tag/owner",
		"rds:cluster-pg-tag/owner", "rds:snapshot-tag/owner", "rds:og-tag/owner",
	} {
		if got, ok := instance[absent]; ok {
			t.Errorf("a DB instance target settled %s = %v; that key belongs to another resource type", absent, got)
		}
	}

	cluster := map[string][]string{}
	iamPopulateServiceResourceTags(formRequest(map[string]string{
		"Action": "ModifyDBCluster", "DBClusterIdentifier": "probe-cluster",
	}), "rds", cluster)
	if got, ok := cluster["rds:db-tag/owner"]; ok {
		t.Errorf("a DB cluster target settled rds:db-tag/owner = %v; that key is the DB instance's", got)
	}
}

// TestIAMRDSResourceTagsAreUnsetWithoutAResource holds the no-fallback rule: a
// request that names no RDS resource, or one the simulator does not hold,
// leaves every key unset — what real AWS does when the resource is absent.
func TestIAMRDSResourceTagsAreUnsetWithoutAResource(t *testing.T) {
	buildConformanceSimulator(t)
	rdsResourceTagFixtures()

	cases := []struct {
		name string
		form map[string]string
	}{
		{"an instance that does not exist", map[string]string{
			"Action": "ModifyDBInstance", "DBInstanceIdentifier": "no-such-db"}},
		{"an ARN of an instance that does not exist", map[string]string{
			"Action": "ListTagsForResource", "ResourceName": rdsInstanceARN("no-such-db")}},
		{"an ARN of a resource type the simulator stores no tags on", map[string]string{
			"Action": "ListTagsForResource", "ResourceName": "arn:aws:rds:us-east-1:123456789012:ri:probe-ri"}},
		{"a request naming no resource at all", map[string]string{
			"Action": "DescribeDBEngineVersions", "Engine": "postgres"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := map[string][]string{}
			iamPopulateServiceResourceTags(formRequest(c.form), "rds", ctx)
			assertExactConditionContext(t, ctx, map[string][]string{})
		})
	}
}

// TestIAMRDSResourceTagsPreferTheResourceTheRequestActsOn covers the request
// that names two resources: a restore names the instance it is about to create
// (which is not stored yet) alongside the snapshot it restores from, so the
// snapshot is the resource whose tags a policy can be conditioned on.
func TestIAMRDSResourceTagsPreferTheResourceTheRequestActsOn(t *testing.T) {
	buildConformanceSimulator(t)
	rdsResourceTagFixtures()

	ctx := map[string][]string{}
	iamPopulateServiceResourceTags(formRequest(map[string]string{
		"Action":               "RestoreDBInstanceFromDBSnapshot",
		"DBInstanceIdentifier": "restored-db",
		"DBSnapshotIdentifier": "probe-snap",
	}), "rds", ctx)
	assertExactConditionContext(t, ctx, map[string][]string{
		"aws:ResourceTag/owner":  {"backups"},
		"rds:snapshot-tag/owner": {"backups"},
	})

	// An existing instance modified while also naming its parameter group is a
	// call on the instance.
	ctx = map[string][]string{}
	iamPopulateServiceResourceTags(formRequest(map[string]string{
		"Action":               "ModifyDBInstance",
		"DBInstanceIdentifier": "probe-db",
		"DBParameterGroupName": "probe-pg",
	}), "rds", ctx)
	assertExactConditionContext(t, ctx, map[string][]string{
		"aws:ResourceTag/owner": {"platform"},
		"rds:db-tag/owner":      {"platform"},
	})
}

// TestRDSRequestTagConditionKey covers rds:req-tag/${TagKey} — RDS's own
// spelling of the tags a request carries. RDS's awsQuery tag member is
// Tags.Tag.N, so a policy requiring a tag on create had no key to read.
func TestRDSRequestTagConditionKey(t *testing.T) {
	ctx := queryServiceConditionContext(t, "rds", "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": {"d"},
		"Engine":               {"postgres"},
		"Tags.Tag.1.Key":       {"team"}, "Tags.Tag.1.Value": {"orders"},
		"Tags.Tag.2.Key": {"env"}, "Tags.Tag.2.Value": {"prod"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{
		"rds:req-tag/team": {"orders"},
		"rds:req-tag/env":  {"prod"},
	})

	// A request carrying no tags settles no rds:req-tag key.
	ctx = queryServiceConditionContext(t, "rds", "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": {"d"},
		"Engine":               {"postgres"},
	})
	for key := range ctx {
		if strings.HasPrefix(key, "rds:req-tag/") {
			t.Errorf("a request with no tags settled %s", key)
		}
	}
}

// TestIAMResourceTagsForIAMsOwnResources covers the service the dispatcher had
// no case for at all: a request against an IAM user, role, policy or instance
// profile settled neither aws:ResourceTag/<k> nor iam:ResourceTag/<k>, although
// the simulator stores tags on all four.
func TestIAMResourceTagsForIAMsOwnResources(t *testing.T) {
	buildConformanceSimulator(t)

	iamRoles.Put("probe-tagged-role", IAMRole{
		RoleName: "probe-tagged-role",
		Arn:      "arn:aws:iam::" + awsAccountID() + ":role/probe-tagged-role",
		Tags:     []IAMTag{{Key: "owner", Value: "platform"}},
	})
	iamRoles.Put("probe-untagged-role", IAMRole{
		RoleName: "probe-untagged-role",
		Arn:      "arn:aws:iam::" + awsAccountID() + ":role/probe-untagged-role",
	})
	iamUsers.Put("probe-tagged-user", IAMUser{
		UserName: "probe-tagged-user",
		Arn:      "arn:aws:iam::" + awsAccountID() + ":user/probe-tagged-user",
		Tags:     []IAMTag{{Key: "owner", Value: "identity"}},
	})
	policyArn := "arn:aws:iam::" + awsAccountID() + ":policy/probe-tagged-policy"
	iamPolicies.Put(policyArn, IAMPolicy{
		PolicyName: "probe-tagged-policy", Arn: policyArn,
		Tags: []IAMTag{{Key: "owner", Value: "governance"}},
	})
	iamInstanceProfiles.Put("probe-profile", IAMInstanceProfile{
		InstanceProfileName: "probe-profile",
		Arn:                 "arn:aws:iam::" + awsAccountID() + ":instance-profile/probe-profile",
	})
	iamInstanceProfileTag.Put("probe-profile", IAMInstanceProfileTagSet{
		InstanceProfileName: "probe-profile",
		Tags:                []IAMTag{{Key: "owner", Value: "compute"}},
	})

	cases := []struct {
		name string
		form map[string]string
		want map[string][]string
	}{
		// The reference declares iam:ResourceTag/${TagKey} on the role and user
		// resources, so both spellings are written for them.
		{"a tagged role", map[string]string{"Action": "TagRole", "RoleName": "probe-tagged-role"},
			map[string][]string{
				"aws:ResourceTag/owner": {"platform"},
				"iam:ResourceTag/owner": {"platform"},
			}},
		{"a tagged user", map[string]string{"Action": "GetUser", "UserName": "probe-tagged-user"},
			map[string][]string{
				"aws:ResourceTag/owner": {"identity"},
				"iam:ResourceTag/owner": {"identity"},
			}},
		// The policy and instance-profile resources declare
		// aws:ResourceTag/${TagKey} alone, so no iam: spelling is invented.
		{"a tagged managed policy", map[string]string{"Action": "GetPolicy", "PolicyArn": policyArn},
			map[string][]string{"aws:ResourceTag/owner": {"governance"}}},
		{"a tagged instance profile", map[string]string{"Action": "GetInstanceProfile", "InstanceProfileName": "probe-profile"},
			map[string][]string{"aws:ResourceTag/owner": {"compute"}}},
		// An untagged role has no tags to expose, and an absent role has no
		// resource at all: both leave the keys unset rather than defaulted.
		{"an untagged role", map[string]string{"Action": "TagRole", "RoleName": "probe-untagged-role"},
			map[string][]string{}},
		{"a role that does not exist", map[string]string{"Action": "TagRole", "RoleName": "no-such-role"},
			map[string][]string{}},
		{"a request naming no IAM resource", map[string]string{"Action": "ListRoles"},
			map[string][]string{}},
		// AttachRolePolicy is authorized against the role, not the policy it
		// attaches, so the role's tags are the ones a condition reads.
		{"a policy attached to a role", map[string]string{
			"Action": "AttachRolePolicy", "RoleName": "probe-tagged-role", "PolicyArn": policyArn},
			map[string][]string{
				"aws:ResourceTag/owner": {"platform"},
				"iam:ResourceTag/owner": {"platform"},
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := map[string][]string{}
			if !iamPopulateServiceResourceTags(formRequest(c.form), "iam", ctx) {
				t.Fatal("the dispatcher did not handle iam")
			}
			assertExactConditionContext(t, ctx, c.want)
		})
	}
}
