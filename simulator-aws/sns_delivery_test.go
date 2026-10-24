package main

import (
	"testing"
	"time"
)

func TestSNSPredefinedDeliveryPolicies(t *testing.T) {
	for name, c := range map[string]struct {
		policy snsRetryPolicy
		waits  map[int]time.Duration
	}{
		"AWS managed endpoints": {snsManagedEndpointRetryPolicy, map[int]time.Duration{
			1: 0, 3: 0, 4: time.Second, 5: time.Second, 15: 20 * time.Second, 16: 20 * time.Second, 100015: 20 * time.Second,
		}},
		"customer managed endpoints": {snsCustomerEndpointRetryPolicy, map[int]time.Duration{
			1: 10 * time.Second, 2: 10 * time.Second, 12: 600 * time.Second, 13: 600 * time.Second, 50: 600 * time.Second,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			for retry, want := range c.waits {
				if got := c.policy.delay(retry); got.Round(time.Millisecond) != want {
					t.Errorf("retry %d waits %v, want %v", retry, got, want)
				}
			}
		})
	}
	backoff := snsManagedEndpointRetryPolicy
	for retry := 7; retry <= 15; retry++ {
		if backoff.delay(retry) <= backoff.delay(retry-1) {
			t.Errorf("retry %d waits %v, no longer than retry %d", retry, backoff.delay(retry), retry-1)
		}
	}
}
