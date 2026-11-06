package main

import "github.com/e6qu/sockerless-cloud/sim/cacheengine"

// msRedisConnectPermission is what a principal needs to connect to a cluster
// that authenticates with IAM.
const msRedisConnectPermission = "redis.clusters.connect"

// connectAs is the engine user an access token this simulator issued
// authenticates as, when the principal it names holds redis.clusters.connect
// on the cluster: the ACL policy user its email names, or the default user.
func (p *msRedisPlane) connectAs(credential cacheengine.Credential) (cacheengine.Grant, bool) {
	claims, err := verifiedAccessTokenClaims(credential.Password)
	if err != nil {
		return cacheengine.Grant{}, false
	}
	policies := gcpPolicyChain(p.name)
	held := gcpPermissionsHeldUnder(gcpSubjectPrincipal(claims.Sub), policies, []string{msRedisConnectPermission}, gcpIAMResourceNamed(p.name))
	if len(held) != 1 {
		return cacheengine.Grant{}, false
	}
	return cacheengine.Grant{User: p.engineUserOf(claims.Sub)}, true
}
