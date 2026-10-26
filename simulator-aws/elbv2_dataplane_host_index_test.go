package main

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every request into the simulator passes the data-plane host check, so it
// must not read the whole load-balancer store; count store reads, not time.

type countingELBv2LoadBalancerStore struct {
	sim.Store[ELBv2LoadBalancer]
	lists atomic.Int64
}

func (s *countingELBv2LoadBalancerStore) List() []ELBv2LoadBalancer {
	s.lists.Add(1)
	return s.Store.List()
}

// Restores the package globals afterwards.
func elbv2DataPlaneHostTestStores(t *testing.T) *countingELBv2LoadBalancerStore {
	t.Helper()
	previous := elbv2LoadBalancers
	counting := &countingELBv2LoadBalancerStore{
		Store: sim.MakeStore[ELBv2LoadBalancer](nil, "elbv2_load_balancers"),
	}
	elbv2LoadBalancers = counting
	// Generations are unique per process, so an index built from an earlier store
	// refuses to answer for its replacement.
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
	// Requests the data plane does not own, every API call, must not read the
	// store either.
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

	// A port, uppercase and a trailing root dot name the same load balancer.
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

	// Reachable without explicit invalidation.
	balancers.Put("arn-b", ELBv2LoadBalancer{
		Arn: "arn-b", Name: "app-b", DNSName: "app-b-0987654321.us-east-1.elb.amazonaws.com",
	})
	lb, ok := elbv2LoadBalancerFromDataPlaneHost("app-b-0987654321.us-east-1.elb.amazonaws.com")
	require.True(t, ok)
	require.Equal(t, "arn-b", lb.Arn)

	// A deleted load balancer's hostname stops resolving.
	require.True(t, balancers.Delete("arn-a"))
	_, ok = elbv2LoadBalancerFromDataPlaneHost("app-a-1234567890.us-east-1.elb.amazonaws.com")
	require.False(t, ok, "a deleted load balancer still answered on its hostname")

	// A load balancer without a DNS name must not match an empty host.
	balancers.Put("arn-c", ELBv2LoadBalancer{Arn: "arn-c", Name: "app-c"})
	_, ok = elbv2LoadBalancerFromDataPlaneHost("")
	require.False(t, ok, "an empty host resolved to a load balancer with no DNS name")
}
