package main

import "strings"

// msRedisAclPolicyOutcome is a cluster's aclPolicyInfo once it applied
// policy's current revision, or failed to with err. The revisions it applied
// before stay listed.
func msRedisAclPolicyOutcome(prior *MSRedisAclPolicyInfo, policy MSRedisAclPolicy, err error) *MSRedisAclPolicyInfo {
	revision := policy.Name + "/revisions/" + policy.Version
	info := MSRedisAclPolicyInfo{}
	if prior != nil && prior.AppliedAclPolicy == policy.Name {
		info.AppliedAclPolicy = prior.AppliedAclPolicy
		info.AppliedAclPolicyRevision = prior.AppliedAclPolicyRevision
		info.AppliedAclPolicyRevisionNumber = prior.AppliedAclPolicyRevisionNumber
		for _, status := range prior.AclPolicyRevisionStatuses {
			if status.AclPolicyRevision != revision {
				info.AclPolicyRevisionStatuses = append(info.AclPolicyRevisionStatuses, status)
			}
		}
	}
	status := MSRedisAclPolicyRevisionStatus{AclPolicyRevision: revision, AclPolicyRevisionNumber: policy.Version, State: "APPLIED"}
	if err != nil {
		status.State, status.ErrorMessage = "FAILED", err.Error()
	} else {
		info.AppliedAclPolicy = policy.Name
		info.AppliedAclPolicyRevision = revision
		info.AppliedAclPolicyRevisionNumber = policy.Version
	}
	info.AclPolicyRevisionStatuses = append(info.AclPolicyRevisionStatuses, status)
	return &info
}

// msRedisApplyAclPolicyRevision sets policy's current revision on every
// cluster it is attached to and records each cluster's outcome.
func msRedisApplyAclPolicyRevision(policy MSRedisAclPolicy) {
	for _, cluster := range msRedisClusters.List() {
		if cluster.AclPolicy != policy.Name {
			continue
		}
		err := msRedisApplyUsers(cluster.Name)
		msRedisClusters.Update(cluster.Name, func(c *MSRedisCluster) {
			c.AclPolicyInfo = msRedisAclPolicyOutcome(c.AclPolicyInfo, policy, err)
		})
	}
}

// msRedisAclPolicyView is policy as the API reports it, with the status of
// each cluster it is attached to.
func msRedisAclPolicyView(policy MSRedisAclPolicy) MSRedisAclPolicy {
	policy.ClusterAclPolicyAttachments = nil
	for _, cluster := range msRedisClusters.List() {
		if cluster.AclPolicy != policy.Name {
			continue
		}
		attachment := MSRedisClusterAclPolicyAttachment{Cluster: cluster.Name}
		if cluster.AclPolicyInfo != nil {
			attachment.AclPolicyRevisionStatuses = cluster.AclPolicyInfo.AclPolicyRevisionStatuses
		}
		policy.ClusterAclPolicyAttachments = append(policy.ClusterAclPolicyAttachments, attachment)
	}
	return policy
}

// msRedisAclRevisionView is revision as the API reports it, with the
// clusters that run it.
func msRedisAclRevisionView(revision MSRedisAclPolicyRevision) MSRedisAclPolicyRevision {
	revision.AttachedClusters = nil
	policy, _, _ := strings.Cut(revision.Name, "/revisions/")
	for _, cluster := range msRedisClusters.List() {
		if cluster.AclPolicy == policy && cluster.AclPolicyInfo != nil &&
			cluster.AclPolicyInfo.AppliedAclPolicyRevision == revision.Name {
			revision.AttachedClusters = append(revision.AttachedClusters, cluster.Name)
		}
	}
	return revision
}
