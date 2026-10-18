package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// exportRootFS writes the guest's root filesystem to w as a tar archive, read
// from its ext4 image without mounting it. A running guest's filesystem is
// frozen for the read, which writes every dirty block and the journal out
// and holds writers until the thaw, so the archive is the disk as a clean
// unmount would leave it.
func (g *guest) exportRootFS(ctx context.Context, w io.Writer) error {
	g.mu.Lock()
	running, rootfs := g.status == statusRunning, g.rootfs
	g.mu.Unlock()
	if !rootfs {
		return fmt.Errorf("%w: the guest has no root filesystem", errGuestState)
	}
	tree, err := os.MkdirTemp(g.dir, "export-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tree) }()
	if running {
		if err := g.guestPost(ctx, "/freeze"); err != nil {
			return fmt.Errorf("freeze the guest's filesystem: %w", err)
		}
		err = dumpExt4(ctx, filepath.Join(g.dir, "rootfs.ext4"), tree)
		if thawErr := g.guestPost(context.WithoutCancel(ctx), "/thaw"); thawErr != nil && err == nil {
			err = fmt.Errorf("thaw the guest's filesystem: %w", thawErr)
		}
	} else {
		err = dumpExt4(ctx, filepath.Join(g.dir, "rootfs.ext4"), tree)
	}
	if err != nil {
		return err
	}
	return tarTree(ctx, tree, w)
}

// guestPost sends guestinit a request with no body.
func (g *guest) guestPost(ctx context.Context, path string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest"+path, nil)
	resp, err := g.vsockClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	return nil
}

// dumpExt4 copies the filesystem in image into dir with debugfs. It dumps
// each entry of the root in turn: debugfs names what it dumps after the
// source path's last element, and the root has none.
func dumpExt4(ctx context.Context, image, dir string) error {
	listing, err := exec.CommandContext(ctx, "debugfs", "-R", "ls -p /", image).Output()
	if err != nil {
		return fmt.Errorf("debugfs ls %s: %w", image, err)
	}
	var script strings.Builder
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "/")
		if len(fields) < 7 {
			continue
		}
		switch name := fields[5]; name {
		case "", ".", "..", "lost+found":
		default:
			fmt.Fprintf(&script, "rdump /%s %s\n", name, dir)
		}
	}
	commands, err := os.CreateTemp(filepath.Dir(dir), "debugfs-*.cmd")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(commands.Name()) }()
	if _, err := commands.WriteString(script.String()); err != nil {
		_ = commands.Close()
		return err
	}
	if err := commands.Close(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "debugfs", "-f", commands.Name(), image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs rdump %s: %w: %s", image, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "rdump:") {
			return fmt.Errorf("debugfs rdump %s: %s", image, strings.TrimSpace(line))
		}
	}
	return nil
}

// tarTree streams dir as an archive with numeric owners, which is what the
// engine imports as an image.
func tarTree(ctx context.Context, dir string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "tar", "--numeric-owner", "-C", dir, "-cf", "-", ".")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
