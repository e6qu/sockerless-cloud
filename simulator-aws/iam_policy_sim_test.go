package main

import "testing"

// A policy written the way CloudWatch Logs policies are always written — with
// the trailing ":*" that DescribeLogGroups puts on a log group's ARN — must
// authorize the log-group-level actions. This is exactly the grant the
// ecs-dev-desktop control plane holds for /edd-dev/workspaces, and matching only
// the Service Reference's suffix-less form denied FilterLogEvents while claiming
// the ACTION was not allowed.
func TestIAMLogGroupPolicySuffixAuthorizesLogGroupActions(t *testing.T) {
	const group = "arn:aws:logs:us-east-1:123456789012:log-group:/edd-dev/workspaces"
	stmt := iamStatement{Resource: []string{group + ":*"}}

	if !iamResourceMatches(stmt, group) {
		t.Errorf("policy resource %q must authorize the log group %q", group+":*", group)
	}
	// A grant on the group covers its streams, as it does in AWS.
	if !iamResourceMatches(stmt, group+":log-stream:workspace/workspace/abc") {
		t.Errorf("policy resource %q must authorize a stream inside the group", group+":*")
	}
	// The widening stops at the group: another group is still out of scope.
	other := "arn:aws:logs:us-east-1:123456789012:log-group:/edd-dev/control-plane"
	if iamResourceMatches(stmt, other) {
		t.Errorf("policy resource %q must not authorize a different log group %q", group+":*", other)
	}
	// A stream-scoped grant must not widen to the whole group.
	streamStmt := iamStatement{Resource: []string{group + ":log-stream:one:*"}}
	if iamResourceMatches(streamStmt, group) {
		t.Error("a grant scoped to one log stream must not authorize the whole log group")
	}
}
