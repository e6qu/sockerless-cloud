package main

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Deciding whether a request belongs to a load balancer's data plane happens in
// a handler wrapper, ahead of every service's own handler, so every request
// into the simulator pays it — an Amazon DynamoDB call as much as a proxied
// page load. Reading the whole load-balancer store to answer it is the same
// defect the Amazon ECS task scan was, on a hotter path, so it is measured the
// same way: by counting reads of the store rather than by timing anything.

// countingELBv2LoadBalancerStore counts List calls and delegates the rest.
type countingELBv2LoadBalancerStore struct {
	sim.Store[ELBv2LoadBalancer]
	lists atomic.Int64
}

func (s *countingELBv2LoadBalancerStore) List() []ELBv2LoadBalancer {
	s.lists.Add(1)
	return s.Store.List()
}

// elbv2DataPlaneHostTestStores gives one test its own load-balancer store and
// an unbuilt hostname index, and restores the package globals afterwards.
func elbv2DataPlaneHostTestStores(t *testing.T) *countingELBv2LoadBalancerStore {
	t.Helper()
	previous := elbv2LoadBalancers
	counting := &countingELBv2LoadBalancerStore{
		Store: sim.MakeStore[ELBv2LoadBalancer](nil, "elbv2_load_balancers"),
	}
	elbv2LoadBalancers = counting
	// No index reset: generations are unique across every store in the
	// process, so the index built from whatever store ran before this refuses
	// to answer for the replacement rather than being served for it.
	t.Cleanup(func() { elbv2LoadBalancers = previous })
	return counting
}

func TestELBv2DataPlaneHostReadsTheLoadBalancerStoreOncePerChange(t *testing.T) {
	balancers := elbv2DataPlaneHostTestStores(t)
	balancers.Put("arn-a", ELBv2LoadBalancer{
		Arn: "arn-a", Name: "app-a", DNSName: "app-a-1234567890.us-east-1.elb.amazonaws.com",
	})

	readsAfterSetup := balancers.lists.Load()
	for range 25 {
		lb, ok := elbv2LoadBalancerFromDataPlaneHost("app-a-1234567890.us-east-1.elb.amazonaws.com")
		require.True(t, ok)
		require.Equal(t, "arn-a", lb.Arn)
	}
	// A request the data plane does not own is the common case — every API
	// call — and must not read the store either.
	for range 25 {
		_, ok := elbv2LoadBalancerFromDataPlaneHost("dynamodb.us-east-1.amazonaws.com")
		require.False(t, ok)
	}
	require.Equal(t, readsAfterSetup+1, balancers.lists.Load(),
		"fifty data-plane host lookups against an unchanged store must read it once")
}

func TestELBv2DataPlaneHostIndexFollowsTheLoadBalancerStore(t *testing.T) {
	balancers := elbv2DataPlaneHostTestStores(t)
	balancers.Put("arn-a", ELBv2LoadBalancer{
		Arn: "arn-a", Name: "app-a", DNSName: "app-a-1234567890.us-east-1.elb.amazonaws.com",
	})

	// The host header carries a port, an uppercase spelling and a trailing
	// root dot in the wild; all three name the same load balancer.
	for _, host := range []string{
		"app-a-1234567890.us-east-1.elb.amazonaws.com",
		"app-a-1234567890.us-east-1.elb.amazonaws.com:8080",
		"APP-A-1234567890.US-EAST-1.ELB.AMAZONAWS.COM",
		"app-a-1234567890.us-east-1.elb.amazonaws.com.",
	} {
		lb, ok := elbv2LoadBalancerFromDataPlaneHost(host)
		require.Truef(t, ok, "host %q did not resolve to its load balancer", host)
		require.Equal(t, "arn-a", lb.Arn)
	}

	// A second load balancer is reachable without any explicit invalidation.
	balancers.Put("arn-b", ELBv2LoadBalancer{
		Arn: "arn-b", Name: "app-b", DNSName: "app-b-0987654321.us-east-1.elb.amazonaws.com",
	})
	lb, ok := elbv2LoadBalancerFromDataPlaneHost("app-b-0987654321.us-east-1.elb.amazonaws.com")
	require.True(t, ok)
	require.Equal(t, "arn-b", lb.Arn)

	// A deleted load balancer leaves the index on the next lookup, and its
	// hostname stops resolving.
	require.True(t, balancers.Delete("arn-a"))
	_, ok = elbv2LoadBalancerFromDataPlaneHost("app-a-1234567890.us-east-1.elb.amazonaws.com")
	require.False(t, ok, "a deleted load balancer still answered on its hostname")

	// A load balancer still being created has no DNS name yet, and the empty
	// host of a malformed request must not resolve to it.
	balancers.Put("arn-c", ELBv2LoadBalancer{Arn: "arn-c", Name: "app-c"})
	_, ok = elbv2LoadBalancerFromDataPlaneHost("")
	require.False(t, ok, "an empty host resolved to a load balancer with no DNS name")
}
