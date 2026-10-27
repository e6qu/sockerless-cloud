package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// With configuration.dapr.enabled, Azure Container Apps injects a daprd sidecar
// into every replica: the Dapr HTTP API on localhost:3500, gRPC on
// localhost:50001, the app known by its appId and reached on its appPort.

// The flag spellings below (`--max-body-size`, `--read-buffer-size`) are
// verified against this exact release; others spell them differently.
const daprdSidecarImage = "mirror.gcr.io/daprio/daprd:1.18.2"

func containerAppDaprSpec(app ContainerApp) *ContainerAppDapr {
	cfg := app.Properties.Configuration
	if cfg == nil || cfg.Dapr == nil || cfg.Dapr.Enabled == nil || !*cfg.Dapr.Enabled {
		return nil
	}
	return cfg.Dapr
}

// acaAppDaprContainer joins the replica's network namespace, so localhost:3500
// and :50001 reach it from every container in the replica, and logs to
// ContainerAppConsoleLogs_CL as Azure Container Apps does. It belongs to the
// replica set, so the app's lifecycle tears it down.
func acaAppDaprContainer(app ContainerApp, d *ContainerAppDapr, replica string) workload.Container {
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
	// Azure Container Apps takes httpMaxRequestSize in MB and httpReadBufferSize in
	// KB; daprd takes resource quantities.
	if d.HTTPMaxRequestSize != nil {
		args = append(args, "--max-body-size", fmt.Sprintf("%dMi", *d.HTTPMaxRequestSize))
	}
	if d.HTTPReadBufferSize != nil {
		args = append(args, "--read-buffer-size", fmt.Sprintf("%dKi", *d.HTTPReadBufferSize))
	}
	shortName := app.Name
	if len(shortName) > 24 {
		shortName = shortName[:24]
	}
	return workload.Container{Name: "daprd", Config: sim.ContainerConfig{
		CancelGracePeriod: acaAppStopGrace(app),
		Image:             daprdSidecarImage,
		// The image declares no entrypoint; the binary is /daprd.
		Command: []string{"/daprd"},
		Args:    args,
		Name:    fmt.Sprintf("sockerless-sim-azure-app-%s-%s-daprd", shortName, replica[strings.LastIndex(replica, "-")+1:]),
		Labels: map[string]string{
			"sockerless-sim-type": "aca-app-dapr-sidecar",
			"sockerless-app-id":   app.ID,
			"sockerless-app-name": app.Name,
		},
		Sandbox: SandboxACA,
	}}
}
