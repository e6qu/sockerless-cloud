package main

// msRedisConnectPermission is what a principal needs to connect to a cluster
// that authenticates with IAM.
const msRedisConnectPermission = "redis.clusters.connect"

// connectAs is the engine user an access token this simulator issued
// authenticates as, when the principal it names holds redis.clusters.connect
// on the cluster: the ACL policy user its email names, or the default user.
func (p *msRedisPlane) connectAs(token string) (string, bool) {
	claims, err := verifiedAccessTokenClaims(token)
	if err != nil {
		return "", false
	}
	principal, owner := gcpSubjectPrincipal(claims.Sub)
	policies := gcpHierarchyPolicies(resourceProject(p.name))
	held := gcpPermissionsHeldUnder(principal, owner, policies, []string{msRedisConnectPermission}, gcpIAMResourceNamed(p.name))
	if len(held) != 1 {
		return "", false
	}
	return p.engineUserOf(claims.Sub), true
}
