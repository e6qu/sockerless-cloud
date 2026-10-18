// Command nodeagent-image builds the node agent's image on the engine, or
// finds it held, and prints its name. A host runs it before starting a
// simulator that places a service on microVMs, so the simulator's start does
// not wait on the build.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
)

func main() {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeagent-image: %v\n", err)
		os.Exit(1)
	}
	image, platform, err := nodeagent.EnsureImage(context.Background(), cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeagent-image: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s %s\n", image, platform)
}
