package registrytrust

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const commandTimeout = 30 * time.Second

// ConfigureLoopbackHTTPRegistry makes one loopback HTTP registry coordinate
// trusted by the container engine. Docker trusts loopback registries natively;
// Podman requires a scoped registries.conf.d entry.
func ConfigureLoopbackHTTPRegistry(ctx context.Context, coordinate string) (func() error, error) {
	host, _, err := net.SplitHostPort(coordinate)
	if err != nil {
		return nil, fmt.Errorf("parse registry coordinate %q: %w", coordinate, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("registry coordinate %q is not loopback", coordinate)
	}

	componentsOutput, err := run(ctx, "docker", "version", "--format", "{{json .Server.Components}}")
	if err != nil {
		return nil, fmt.Errorf("inspect container runtime components: %w: %s", err, componentsOutput)
	}
	var components []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(componentsOutput, &components); err != nil {
		return nil, fmt.Errorf("decode container runtime components: %w: %s", err, componentsOutput)
	}
	isPodman := false
	for _, component := range components {
		if component.Name == "Podman Engine" {
			isPodman = true
			break
		}
	}
	if !isPodman {
		return func() error { return nil }, nil
	}

	// The Linux container harness reaches the host Podman engine through its
	// Docker-compatible socket. Podman treats loopback registries as insecure
	// on that engine already; configuration files and a user systemd instance
	// inside the client container cannot configure or reload the remote engine.
	if runtime.GOOS == "linux" && insideContainer() {
		return func() error { return nil }, nil
	}

	content := "[[registry]]\nlocation = " + strconv.Quote(coordinate) + "\ninsecure = true\n"
	name := fmt.Sprintf("sockerless-sdk-test-%d.conf", os.Getpid())
	cleanup, err := configurePodman(ctx, name, content)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		info, infoErr := run(ctx, "docker", "info")
		if infoErr == nil && strings.Contains(string(info), coordinate) {
			return cleanup, nil
		}
		if time.Now().After(deadline) {
			_ = cleanup()
			return nil, fmt.Errorf("container runtime Podman did not load registry trust for %s: %v: %s", coordinate, infoErr, info)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func insideContainer() bool {
	for _, marker := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func configurePodman(ctx context.Context, name, content string) (func() error, error) {
	if runtime.GOOS != "darwin" {
		configHome, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve Podman configuration directory: %w", err)
		}
		directory := filepath.Join(configHome, "containers", "registries.conf.d")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create Podman registry configuration directory: %w", err)
		}
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("write Podman registry configuration: %w", err)
		}
		if output, err := run(ctx, "systemctl", "--user", "stop", "podman.service"); err != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("reload rootless Podman registry configuration: %w: %s", err, output)
		}
		return func() error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			_, err := run(ctx, "systemctl", "--user", "stop", "podman.service")
			return err
		}, nil
	}

	machineOutput, err := run(ctx, "podman", "machine", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("list Podman machines: %w: %s", err, machineOutput)
	}
	var machines []struct {
		Name    string `json:"Name"`
		Running bool   `json:"Running"`
	}
	if err := json.Unmarshal(machineOutput, &machines); err != nil {
		return nil, fmt.Errorf("decode Podman machine list: %w: %s", err, machineOutput)
	}
	machine := ""
	for _, candidate := range machines {
		if !candidate.Running {
			continue
		}
		if machine != "" {
			return nil, fmt.Errorf("multiple Podman machines are running; the Docker-compatible connection is ambiguous")
		}
		machine = candidate.Name
	}
	if machine == "" {
		return nil, fmt.Errorf("container runtime reported Podman Engine but no Podman machine is running")
	}

	local, err := os.CreateTemp("", name)
	if err != nil {
		return nil, fmt.Errorf("create temporary Podman registry configuration: %w", err)
	}
	localPath := local.Name()
	defer func() { _ = os.Remove(localPath) }()
	if _, err := local.WriteString(content); err != nil {
		_ = local.Close()
		return nil, fmt.Errorf("write temporary Podman registry configuration: %w", err)
	}
	if err := local.Close(); err != nil {
		return nil, fmt.Errorf("close temporary Podman registry configuration: %w", err)
	}

	path := "/etc/containers/registries.conf.d/" + name
	remotePath := "/tmp/" + name
	commands := [][]string{
		{"podman", "machine", "cp", "--quiet", localPath, machine + ":" + remotePath},
		{"podman", "machine", "ssh", machine, "sudo", "mkdir", "-p", "/etc/containers/registries.conf.d"},
		{"podman", "machine", "ssh", machine, "sudo", "install", "-m", "0644", remotePath, path},
	}
	for _, command := range commands {
		if output, err := run(ctx, command[0], command[1:]...); err != nil {
			return nil, fmt.Errorf("configure Podman registry trust: %w: %s", err, output)
		}
	}
	stored, err := run(ctx, "podman", "machine", "ssh", machine, "sudo", "cat", path)
	if err != nil {
		return nil, fmt.Errorf("read Podman registry configuration: %w: %s", err, stored)
	}
	if string(stored) != content {
		return nil, fmt.Errorf("container runtime Podman registry configuration was not written exactly: %s", stored)
	}
	if output, err := run(ctx, "podman", "machine", "ssh", machine, "sudo", "systemctl", "stop", "podman.service"); err != nil {
		return nil, fmt.Errorf("reload Podman registry configuration: %w: %s", err, output)
	}

	return func() error {
		if output, err := run(ctx, "podman", "machine", "ssh", machine, "sudo", "rm", "-f", path, remotePath); err != nil {
			return fmt.Errorf("remove Podman registry configuration: %w: %s", err, output)
		}
		output, err := run(ctx, "podman", "machine", "ssh", machine, "sudo", "systemctl", "stop", "podman.service")
		if err != nil {
			return fmt.Errorf("reload Podman registry configuration after cleanup: %w: %s", err, output)
		}
		return nil
	}, nil
}

func run(parent context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	return command.CombinedOutput()
}
