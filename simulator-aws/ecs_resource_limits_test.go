package main

import "testing"

func TestECSContainerResourceLimitsMatchAdvertisedTaskSize(t *testing.T) {
	mem, cpu := ecsContainerResourceLimits(
		ECSTaskDefinition{Cpu: "512", Memory: "1024"},
		ECSContainerDefinition{},
	)
	if mem != 1024*1024*1024 {
		t.Fatalf("memory limit = %d, want 1024 MiB in bytes", mem)
	}
	if cpu != 500_000_000 {
		t.Fatalf("CPU limit = %d, want 0.5 vCPU in NanoCPUs", cpu)
	}
}

func TestECSContainerResourceLimitsPreferContainerDefinition(t *testing.T) {
	mem, cpu := ecsContainerResourceLimits(
		ECSTaskDefinition{Cpu: "1024", Memory: "2048"},
		ECSContainerDefinition{Cpu: 256, Memory: 512},
	)
	if mem != 512*1024*1024 {
		t.Fatalf("memory limit = %d, want 512 MiB in bytes", mem)
	}
	if cpu != 250_000_000 {
		t.Fatalf("CPU limit = %d, want 0.25 vCPU in NanoCPUs", cpu)
	}
}
