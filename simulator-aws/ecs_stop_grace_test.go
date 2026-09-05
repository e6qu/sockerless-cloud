package main

import (
	"testing"
	"time"
)

// A container's stop grace is its definition's stopTimeout; without one it is
// the thirty seconds Amazon ECS waits by default. Zero is a real setting — kill
// at once — and not the absence of one.
func TestECSContainerStopGraceReadsTheDefinition(t *testing.T) {
	two, zero := 2, 0
	cases := map[string]struct {
		cd   ECSContainerDefinition
		want time.Duration
	}{
		"unset": {ECSContainerDefinition{Name: "app"}, 30 * time.Second},
		"set":   {ECSContainerDefinition{Name: "app", StopTimeout: &two}, 2 * time.Second},
		"zero":  {ECSContainerDefinition{Name: "app", StopTimeout: &zero}, 0},
	}
	for name, c := range cases {
		if got := ecsContainerStopGrace(c.cd); got != c.want {
			t.Errorf("%s: stop grace = %s, want %s", name, got, c.want)
		}
	}
	graces := ecsTaskStopGrace(ECSTaskDefinition{ContainerDefinitions: []ECSContainerDefinition{
		{Name: "app", StopTimeout: &two}, {Name: "log-router"},
	}})
	if graces["app"] != 2*time.Second || graces["log-router"] != 30*time.Second {
		t.Fatalf("task stop graces = %v", graces)
	}
	if _, present := graces["__pause__"]; present {
		t.Fatal("the pause container is not a definition and gets no grace")
	}
}
