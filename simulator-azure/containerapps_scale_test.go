package main

import (
	"reflect"
	"testing"
	"time"
)

// A revision scales on its HTTP rule, or on the default one when it has
// ingress and names none, and on its queue rules; a revision with neither runs
// its minimum, and one whose only rules the simulator does not run keeps a
// replica.
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
