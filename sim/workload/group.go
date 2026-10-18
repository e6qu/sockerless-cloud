package workload

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// Container is one member of a container group: the name the cloud's resource
// gives it, for errors, and the configuration to place it with. Place and
// PlaceMember read Config.Architecture off the image, pulling with
// Config.RegistryAuth, so a member never names its own platform.
type Container struct {
	Name   string
	Config sim.ContainerConfig
}

// Group is a running main workload and the members that share its network
// namespace — a Cloud Run task, a Container Apps replica or job execution —
// on whichever backend placed them.
type Group struct {
	Main     placement.Handle
	Sidecars []placement.Handle
}

// Place places c on backend with the platform its image declares; a member
// naming a platform of its own is a caller's mistake. role names the member's
// part in errors.
func Place(ctx context.Context, backend placement.Backend, role string, c Container, sink sim.LogSink) (placement.Handle, error) {
	if err := resolvePlatform(ctx, role, &c); err != nil {
		return nil, err
	}
	handle, err := backend.Place(ctx, c.Config, sink)
	if err != nil {
		return nil, fmt.Errorf("start %s container %q: %w", role, c.Name, err)
	}
	return handle, nil
}

// PlaceMember places c beside the placed workload owner, in its network
// namespace, so a member listening on localhost:<port> is reachable from the
// owner on the same address. A member that names a network of its own is
// refused: it takes the owner's.
func PlaceMember(ctx context.Context, backend placement.Backend, owner string, role string, c Container, sink sim.LogSink) (placement.Handle, error) {
	if c.Config.NetworkMode != "" || c.Config.Network != "" || len(c.Config.NetworkAliases) > 0 || len(c.Config.ExtraHosts) > 0 {
		return nil, fmt.Errorf("%s container %q names its own network; it joins the main container's", role, c.Name)
	}
	if err := resolvePlatform(ctx, role, &c); err != nil {
		return nil, err
	}
	handle, err := backend.PlaceMember(ctx, owner, c.Config, sink)
	if err != nil {
		return nil, fmt.Errorf("start %s container %q: %w", role, c.Name, err)
	}
	return handle, nil
}

func resolvePlatform(ctx context.Context, role string, c *Container) error {
	if c.Config.Architecture != "" {
		return fmt.Errorf("%s container %q names its platform; the group reads it off the image", role, c.Name)
	}
	platform, err := LocalImagePlatform(ctx, c.Config.Image, c.Config.RegistryAuth)
	if err != nil {
		return fmt.Errorf("resolve %s container %q image platform: %w", role, c.Name, err)
	}
	c.Config.Architecture = platform
	return nil
}

// Stop sends main the stop signal, giving it grace to exit before it is
// killed, and then cancels every member. A nil group stops nothing.
func (g *Group) Stop(grace time.Duration) {
	if g == nil {
		return
	}
	if g.Main != nil {
		g.Main.Stop()
		g.Main.Cancel()
	}
	for _, h := range g.Sidecars {
		if h != nil {
			h.Cancel()
		}
	}
}
