package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunTaskReportsContainerTaskImageSizingAndDefaultGroup(t *testing.T) {
	placementHostForTest(t)
	memory := 1024
	tasks, failures, rerr := runECSTasks(context.Background(), ecsRunTaskInput{
		Cluster:        "default",
		TaskDefinition: "percontainer:1",
		Count:          1,
		Overrides: &ECSTaskOverride{ContainerOverrides: []ECSContainerOverride{
			{Name: "app", Memory: &memory},
		}},
	})
	require.Nil(t, rerr)
	require.Empty(t, failures)
	require.Len(t, tasks, 1)
	task := tasks[0]

	// Amazon ECS's documented default when RunTask names no group.
	require.Equal(t, "family:percontainer", task.Group)

	require.Len(t, task.Containers, 2)
	app, side := task.Containers[0], task.Containers[1]
	require.Equal(t, task.TaskArn, app.TaskArn)
	require.Equal(t, "busybox", app.Image)
	require.Equal(t, "512", app.Cpu)
	require.Equal(t, "1024", app.Memory, "the container override replaces the definition's 1536")
	require.Empty(t, app.MemoryReservation)
	require.Equal(t, "512", side.Cpu)
	require.Empty(t, side.Memory)
	require.Equal(t, "512", side.MemoryReservation)
	AwaitSimulatorBackground()
}

func TestRunTaskKeepsTheGroupTheRequestNamed(t *testing.T) {
	placementHostForTest(t)
	tasks, _, rerr := runECSTasks(context.Background(), ecsRunTaskInput{
		Cluster: "default", TaskDefinition: "percontainer:1", Count: 1, Group: "service:web",
	})
	require.Nil(t, rerr)
	require.Len(t, tasks, 1)
	require.Equal(t, "service:web", tasks[0].Group)
	// A container with no CPU units in its definition reports "0", as ECS does.
	require.Equal(t, "0", ecsSizedContainer(ECSContainerDefinition{Name: "x"}).Cpu)
	AwaitSimulatorBackground()
}

func ecsSizedContainer(cd ECSContainerDefinition) ECSTaskContainer {
	var c ECSTaskContainer
	ecsApplyContainerSizing(&c, cd, nil)
	return c
}

func TestRepoDigestForPicksTheReferencesOwnRepository(t *testing.T) {
	require.Equal(t, "sha256:aaa", repoDigestFor("ghcr.io/e6qu/edd/control-plane:abc",
		[]string{"ghcr.io/e6qu/other@sha256:zzz", "ghcr.io/e6qu/edd/control-plane@sha256:aaa"}))
	require.Equal(t, "sha256:bbb", repoDigestFor("alpine:3.22",
		[]string{"docker.io/library/alpine@sha256:bbb"}), "Docker Hub's implicit prefixes compare equal")
	require.Equal(t, "sha256:ccc", repoDigestFor("x/y@sha256:ccc", nil), "a pinned reference names its digest")
	require.Equal(t, "sha256:ddd", repoDigestFor("localhost:5000/app:1",
		[]string{"localhost:5000/app@sha256:ddd"}), "a registry port is not a tag")
	require.Empty(t, repoDigestFor("built-here:dev", nil), "an image never pulled has no repository digest")
}
