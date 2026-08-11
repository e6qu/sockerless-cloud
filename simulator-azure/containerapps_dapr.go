package main

import (
	"context"
	"fmt"
	"strconv"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Dapr sidecar assembly for the Container Apps "Apps" slice. Real Azure
// Container Apps with configuration.dapr.enabled=true injects a daprd
// sidecar container into every replica: the app reaches the Dapr HTTP
// API on localhost:3500 and the gRPC API on localhost:50001, daprd knows
// the app by its configured appId, and forwards service-invocation calls
// to the app's appPort. The sim assembles the same contract with the
// real daprd runtime image joined to the replica's network namespace.

// daprdSidecarImage pins the daprd runtime the sim injects next to app
// replicas. The tag is pinned because the flag spelling below is
// verified against this exact runtime (its body/buffer size flags are
// `--max-body-size` / `--read-buffer-size`; other daprd releases spell
// them differently).
const daprdSidecarImage = "daprio/daprd:1.18.2"

// containerAppDaprSpec returns the app's Dapr configuration when the
// sidecar is enabled, nil otherwise.
func containerAppDaprSpec(app ContainerApp) *ContainerAppDapr {
	cfg := app.Properties.Configuration
	if cfg == nil || cfg.Dapr == nil || cfg.Dapr.Enabled == nil || !*cfg.Dapr.Enabled {
		return nil
	}
	return cfg.Dapr
}

// startACAAppDaprSidecar starts the real daprd sidecar for one replica of
// a container app, sharing the replica's network namespace (Docker
// network-mode container:<main>) so localhost:3500 / localhost:50001
// reach it from every container in the replica — the same pod-local
// contract real ACA provides. Its stdout/stderr stream into the app's
// console log table, matching how ACA surfaces the daprd container's
// lines in ContainerAppConsoleLogs_CL. The handle joins the replica set,
// so stop/replace/delete tear the sidecar down with the app containers.
func startACAAppDaprSidecar(ctx context.Context, resourceID string, app ContainerApp, d *ContainerAppDapr, replica int32, mainContainerID string) (*sim.ContainerHandle, error) {
	args := []string{
		"--dapr-http-port", "3500",
		"--dapr-grpc-port", "50001",
	}
	if d.AppID != "" {
		args = append(args, "--app-id", d.AppID)
	}
	if d.AppPort != nil {
		args = append(args, "--app-port", strconv.Itoa(int(*d.AppPort)))
	}
	if d.AppProtocol != "" {
		args = append(args, "--app-protocol", d.AppProtocol)
	}
	if d.LogLevel != "" {
		args = append(args, "--log-level", d.LogLevel)
	}
	if d.EnableAPILogging != nil && *d.EnableAPILogging {
		args = append(args, "--enable-api-logging")
	}
	// ACA expresses httpMaxRequestSize in MB and httpReadBufferSize in
	// KB; daprd takes both as resource quantities.
	if d.HTTPMaxRequestSize != nil {
		args = append(args, "--max-body-size", fmt.Sprintf("%dMi", *d.HTTPMaxRequestSize))
	}
	if d.HTTPReadBufferSize != nil {
		args = append(args, "--read-buffer-size", fmt.Sprintf("%dKi", *d.HTTPReadBufferSize))
	}

	platform, err := localImagePlatform(ctx, daprdSidecarImage)
	if err != nil {
		return nil, err
	}
	shortName := app.Name
	if len(shortName) > 24 {
		shortName = shortName[:24]
	}
	return sim.StartContainerSync(sim.ContainerConfig{
		Image:        daprdSidecarImage,
		Architecture: platform,
		// The daprd image declares no entrypoint or cmd; the runtime
		// binary lives at /daprd.
		Command: []string{"/daprd"},
		Args:    args,
		Name:    fmt.Sprintf("sockerless-sim-azure-app-%s-%d-daprd-%s", shortName, replica, randomSuffix(6)),
		Labels: map[string]string{
			"sockerless-sim-type": "aca-app-dapr-sidecar",
			"sockerless-app-id":   resourceID,
			"sockerless-app-name": app.Name,
		},
		NetworkMode: "container:" + mainContainerID,
		Sandbox:     sim.SandboxACA,
	}, &acaAppLogSink{appName: app.Name})
}
