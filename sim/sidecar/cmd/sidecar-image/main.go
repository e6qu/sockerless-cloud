// Command sidecar-image builds the network sidecar's image on the engine, or
// finds it held, and prints its name. A host runs it before starting the
// simulators, so a first workload does not wait on the build.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim/sidecar"
)

func main() {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sidecar-image: %v\n", err)
		os.Exit(1)
	}
	image, platform, err := sidecar.EnsureImage(context.Background(), cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sidecar-image: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s %s\n", image, platform)
}
