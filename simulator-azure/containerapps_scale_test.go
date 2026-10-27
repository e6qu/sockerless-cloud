package main

import (
	"reflect"
	"testing"
	"time"
)

// "Scale behavior" in app-containerapps-scale-app.md.gz.
func TestACAScaleInDue(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	const cooldown = 60 * time.Second
	for _, tc := range []struct {
		name                     string
		sinceRaised, sinceActive time.Duration
		desired                  int
		want                     bool
	}{
		{"nonzero inside the window", 299 * time.Second, 0, 1, false},
		{"nonzero after the window", 300 * time.Second, 0, 1, true},
		{"nonzero past the cooldown but inside the window", 120 * time.Second, 0, 2, false},
		{"zero inside the cooldown", 400 * time.Second, 59 * time.Second, 0, false},
		{"zero after the cooldown", 61 * time.Second, 60 * time.Second, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := acaScaleInDue(now, now.Add(-tc.sinceRaised), now.Add(-tc.sinceActive), tc.desired, cooldown); got != tc.want {
				t.Fatalf("acaScaleInDue = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestACARevisionScaling(t *testing.T) {
	containers := []JobContainer{{Name: "app"}}
	i32 := func(n int32) *int32 { return &n }
	for _, tc := range []struct {
		name     string
		scale    *ContainerAppScale
		ingress  bool
		want     acaScaling
		baseline int
	}{
		{"ingress takes the default rule", nil, true,
			acaScaling{max: 10, concurrency: 10, cooldown: 300 * time.Second, polling: 30 * time.Second}, 0},
		{"no ingress and no rule runs the minimum", nil, false,
			acaScaling{max: 10, cooldown: 300 * time.Second, polling: 30 * time.Second}, 0},
		{"an HTTP rule names its concurrency", &ContainerAppScale{MinReplicas: i32(1), MaxReplicas: i32(3), CooldownPeriod: i32(5),
			Rules: []ContainerAppScaleRule{{Name: "http", HTTP: &ContainerAppTriggerScaleRule{Metadata: map[string]string{"concurrentRequests": "2"}}}}}, false,
			acaScaling{min: 1, max: 3, concurrency: 2, cooldown: 5 * time.Second, polling: 30 * time.Second}, 1},
		{"a queue rule scales from zero", &ContainerAppScale{PollingInterval: i32(2),
			Rules: []ContainerAppScaleRule{{Name: "q", AzureQueue: &ContainerAppQueueScaleRule{QueueName: "work"}}}}, false,
			acaScaling{max: 10, queues: []ContainerAppQueueScaleRule{{QueueName: "work"}}, cooldown: 300 * time.Second, polling: 2 * time.Second}, 0},
		{"a TCP rule keeps a replica", &ContainerAppScale{
			Rules: []ContainerAppScaleRule{{Name: "t", TCP: &ContainerAppTriggerScaleRule{}}}}, true,
			acaScaling{max: 10, otherRules: true, cooldown: 300 * time.Second, polling: 30 * time.Second}, 1},
		{"the maximum is at least the minimum", &ContainerAppScale{MinReplicas: i32(4), MaxReplicas: i32(2)}, false,
			acaScaling{min: 4, max: 4, cooldown: 300 * time.Second, polling: 30 * time.Second}, 4},
	} {
		got := acaRevisionScaling(&ContainerAppTemplate{Containers: containers, Scale: tc.scale}, tc.ingress)
		if !reflect.DeepEqual(got, tc.want) || got.baseline() != tc.baseline {
			t.Errorf("%s: scaling %+v with baseline %d, want %+v with %d", tc.name, got, got.baseline(), tc.want, tc.baseline)
		}
	}
}
