package sim

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestADigestPinnedImageAnArchiveRestoredNeedsNoRegistry(t *testing.T) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := pullImage(ctx, cli, containerReaperTestImage, "", ""); err != nil {
		t.Fatal(err)
	}

	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// The registry host does not resolve, so any request to it fails.
	pinned := "sockerless-held.invalid/library/alpine:3@sha256:" + digest
	restored := "sockerless-held.invalid/library/alpine:sha256-" + digest
	if _, err := cli.ImageTag(ctx, client.ImageTagOptions{Source: containerReaperTestImage, Target: restored}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), restored, client.ImageRemoveOptions{})
	})

	if err := pullImage(ctx, cli, pinned, "linux/"+runtime.GOARCH, ""); err != nil {
		t.Fatalf("the restored image did not hold the pinned reference: %v", err)
	}
	id, err := createAndStartContainer(ctx, cli, ContainerConfig{
		Name:         "sockerless-sim-held-digest-" + strings.ToLower(time.Now().Format("150405.000000000")[7:]),
		Image:        pinned,
		Architecture: "linux/" + runtime.GOARCH,
		Args:         []string{"true"},
	})
	if err != nil {
		t.Fatalf("a workload of the pinned reference did not start from the restored image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	})
}
