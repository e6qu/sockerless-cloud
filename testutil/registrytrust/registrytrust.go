package registrytrust

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	loopback, err := isLoopbackCoordinate(coordinate)
	if err != nil {
		return nil, err
	}
	if !loopback {
		return nil, fmt.Errorf("registry coordinate %q is not loopback", coordinate)
	}
	return ConfigureInsecureHTTPRegistry(ctx, coordinate)
}

// ConfigureInsecureHTTPRegistry makes one plain-HTTP registry coordinate
// trusted by the container engine, whichever host it names. A container engine
// that runs in its own virtual machine — Podman on macOS, Docker Desktop —
// cannot reach a listener on the host's loopback address, so a registry served
// by the test process is addressed there by the host-callback name instead, and
// that coordinate needs the trust a loopback one gets for free.
func ConfigureInsecureHTTPRegistry(ctx context.Context, coordinate string) (func() error, error) {
	loopback, err := isLoopbackCoordinate(coordinate)
	if err != nil {
		return nil, err
	}

	isPodman, err := engineIsPodman(ctx)
	if err != nil {
		return nil, err
	}
	if !isPodman {
		if loopback {
			return func() error { return nil }, nil
		}
		return nil, fmt.Errorf("container engine does not trust plain-HTTP registry %q and only Podman's trust can be configured from a test", coordinate)
	}

	// Podman trusts no plain-HTTP registry it was not configured for, loopback
	// included: containers/image pings the registry over HTTPS and only falls
	// back to HTTP for a registry marked insecure in registries.conf, so an
	// unconfigured coordinate fails with "http: server gave HTTP response to
	// HTTPS client". Both branches below write that configuration; neither may
	// return a no-op, because a caller that believes trust was configured when
	// it was not gets that error from the engine much later.
	content := "[[registry]]\nlocation = " + strconv.Quote(coordinate) + "\ninsecure = true\n"
	name := fmt.Sprintf("sockerless-registrytrust-%s-%d.conf", dropInSlug(coordinate), os.Getpid())
	configure := configurePodman
	if insideContainer() {
		// The client container reaches the engine through its
		// Docker-compatible socket and cannot see, let alone write, the
		// filesystem of the host the engine runs on — but the engine can, so
		// the drop-in is written by a container the engine itself creates.
		configure = configurePodmanThroughEngine
	}
	cleanup, err := configure(ctx, name, content)
	if err != nil {
		return nil, err
	}

	// Podman's API service parses registries.conf once, when it starts, so the
	// drop-in reaches the engine at the service's next start. The paths that
	// can stop the service do so above and are confirmed by the first probe;
	// where the service can only be left to exit on its own idle timeout, the
	// probes have to be spaced further apart than that timeout, because a probe
	// is itself API traffic and a tight loop keeps the running service — and
	// its already-parsed configuration — alive indefinitely.
	deadline := time.Now().Add(engineTrustDeadline)
	for {
		info, infoErr := run(ctx, "docker", "info")
		if infoErr == nil && strings.Contains(string(info), coordinate) {
			return cleanup, nil
		}
		if time.Now().After(deadline) {
			_ = cleanup()
			return nil, fmt.Errorf("container runtime Podman did not load registry trust for %s within %s: %v: %s", coordinate, engineTrustDeadline, infoErr, info)
		}
		time.Sleep(engineIdleWindow)
	}
}

const (
	// engineIdleWindow is the quiet period the probes leave the engine, longer
	// than the five-second idle timeout `podman system service` takes by
	// default, so a socket-activated service exits and the next probe starts
	// one that reads the drop-in.
	engineIdleWindow = 7 * time.Second

	// engineTrustDeadline bounds the wait at several of those windows, so a
	// registry the engine will never trust — because another client is holding
	// the service up, or because the drop-in landed somewhere it does not read
	// — fails the test loudly instead of surfacing later as an HTTPS ping
	// against a plain-HTTP registry.
	engineTrustDeadline = 90 * time.Second
)

// ConfigureTrustedRegistryCA makes the container engine trust the certificate
// authority one HTTPS registry coordinate serves, the way an administrator
// trusts a private registry: the PEM is installed in the engine's per-registry
// certificate directory — /etc/containers/certs.d/<host>:<port>/ca.crt for
// Podman (containers-certs.d(5)), /etc/docker/certs.d/<host>:<port>/ca.crt for
// Docker. Both engines read that directory while building the client for a
// request, so trust configured here applies to the very next pull or push,
// which is what makes it usable from a test that shares a long-running engine.
func ConfigureTrustedRegistryCA(ctx context.Context, coordinate string, authority []byte) (func() error, error) {
	if _, _, err := net.SplitHostPort(coordinate); err != nil {
		return nil, fmt.Errorf("parse registry coordinate %q: %w", coordinate, err)
	}
	if len(authority) == 0 {
		return nil, fmt.Errorf("registry %q was given no certificate authority to trust", coordinate)
	}
	isPodman, err := engineIsPodman(ctx)
	if err != nil {
		return nil, err
	}
	root := dockerCertificateDir
	if isPodman {
		root = podmanCertificateDir
	}
	directory := root + "/" + coordinate

	// A registry the engine already trusts would make a test that depends on
	// this call pass without it, so a leftover authority is removed first.
	writer, err := engineHostWriter(ctx)
	if err != nil {
		return nil, err
	}
	if err := writer.removeDirectory(ctx, directory); err != nil {
		return nil, err
	}
	if err := writer.write(ctx, directory, "ca.crt", string(authority)); err != nil {
		return nil, err
	}
	stored, err := writer.read(ctx, directory, "ca.crt")
	if err != nil {
		_ = writer.removeDirectory(ctx, directory)
		return nil, err
	}
	if stored != string(authority) {
		_ = writer.removeDirectory(ctx, directory)
		return nil, fmt.Errorf("registry certificate authority for %s was not installed exactly", coordinate)
	}
	return func() error { return writer.removeDirectory(ctx, directory) }, nil
}

const (
	// podmanCertificateDir is the per-registry certificate directory Podman
	// reads, per containers-certs.d(5).
	podmanCertificateDir = "/etc/containers/certs.d"

	// dockerCertificateDir is the same directory for dockerd, per Docker's
	// "Verify repository client with certificates" documentation.
	dockerCertificateDir = "/etc/docker/certs.d"
)

// engineIsPodman reports whether the container engine this host's docker
// coordinate reaches is Podman, which configures registry trust differently
// from dockerd.
func engineIsPodman(ctx context.Context) (bool, error) {
	componentsOutput, err := run(ctx, "docker", "version", "--format", "{{json .Server.Components}}")
	if err != nil {
		return false, fmt.Errorf("inspect container runtime components: %w: %s", err, componentsOutput)
	}
	var components []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(componentsOutput, &components); err != nil {
		return false, fmt.Errorf("decode container runtime components: %w: %s", err, componentsOutput)
	}
	for _, component := range components {
		if component.Name == "Podman Engine" {
			return true, nil
		}
	}
	return false, nil
}

// isLoopbackCoordinate reports whether a `host:port` registry coordinate names
// the loopback interface, which is the one a container engine running on this
// host trusts over plain HTTP without configuration.
func isLoopbackCoordinate(coordinate string) (bool, error) {
	host, _, err := net.SplitHostPort(coordinate)
	if err != nil {
		return false, fmt.Errorf("parse registry coordinate %q: %w", coordinate, err)
	}
	if host == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}

func insideContainer() bool {
	for _, marker := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// dropInSlug turns a registry coordinate into a filename component, so trust
// for two coordinates configured by one process lands in two drop-ins instead
// of one overwriting the other.
func dropInSlug(coordinate string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, coordinate)
}

// systemRegistriesConfDir is the system drop-in directory a rootful Podman
// engine reads registry configuration from, per containers-registries.conf.d(5).
const systemRegistriesConfDir = "/etc/containers/registries.conf.d"

// registryTrustMount is where the engine host's drop-in directory is mounted
// inside the container that writes the drop-in.
const registryTrustMount = "/registry-trust"

// configurePodmanThroughEngine writes a registries.conf.d drop-in onto the host
// the engine runs on, from a client container that has only the engine's
// Docker-compatible socket.
func configurePodmanThroughEngine(ctx context.Context, name, content string) (func() error, error) {
	writer, err := engineHostWriter(ctx)
	if err != nil {
		return nil, err
	}
	if err := writer.write(ctx, systemRegistriesConfDir, name, content); err != nil {
		return nil, err
	}
	remove := func() error { return writer.removeFile(ctx, systemRegistriesConfDir, name) }
	stored, err := writer.read(ctx, systemRegistriesConfDir, name)
	if err != nil {
		_ = remove()
		return nil, err
	}
	if stored != content {
		_ = remove()
		return nil, fmt.Errorf("container runtime Podman registry configuration was not written exactly: %q", stored)
	}
	return remove, nil
}

// hostWriter writes files onto the filesystem of the host the container engine
// runs on — the only filesystem whose engine configuration that engine reads.
// It has one channel per relationship between this process and that host: the
// filesystem itself when the engine is local; the machine's own management
// connection when the engine lives in a virtual machine this process drives;
// and, from a client container that has nothing but the engine's socket, a
// container the engine creates with the directory bind-mounted — the engine
// being the one party that can reach both sides. That container runs the image
// this process already runs, so nothing has to be pulled for it.
type hostWriter struct {
	image   string
	machine string
}

// engineHostWriter picks how files reach the engine host's filesystem.
func engineHostWriter(ctx context.Context) (*hostWriter, error) {
	if runtime.GOOS == "linux" && !insideContainer() {
		return &hostWriter{}, nil
	}
	if !insideContainer() {
		machine, err := runningPodmanMachine(ctx)
		if err != nil {
			return nil, err
		}
		return &hostWriter{machine: machine}, nil
	}
	securityOptions, err := run(ctx, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return nil, fmt.Errorf("inspect container engine security options: %w: %s", err, securityOptions)
	}
	if strings.Contains(string(securityOptions), "name=rootless") {
		return nil, fmt.Errorf("engine trust cannot be configured on a rootless remote Podman engine: it reads its configuration from the engine user's home directory, which this process cannot address")
	}
	image, err := currentEngineImage(ctx)
	if err != nil {
		return nil, err
	}
	return &hostWriter{image: image}, nil
}

// remote reports whether the writer reaches the engine host indirectly.
func (w *hostWriter) remote() bool { return w.image != "" || w.machine != "" }

// machineWrite installs one file on the machine's filesystem through the
// machine's own copy and shell connections.
func (w *hostWriter) machineWrite(ctx context.Context, directory, name, content string) error {
	local, err := os.CreateTemp("", "sockerless-registrytrust-*")
	if err != nil {
		return fmt.Errorf("create temporary engine configuration: %w", err)
	}
	localPath := local.Name()
	defer func() { _ = os.Remove(localPath) }()
	if _, err := local.WriteString(content); err != nil {
		_ = local.Close()
		return fmt.Errorf("write temporary engine configuration: %w", err)
	}
	if err := local.Close(); err != nil {
		return fmt.Errorf("close temporary engine configuration: %w", err)
	}
	staged := "/tmp/" + filepath.Base(localPath)
	commands := [][]string{
		{"podman", "machine", "cp", "--quiet", localPath, w.machine + ":" + staged},
		{"podman", "machine", "ssh", w.machine, "sudo", "mkdir", "-p", directory},
		{"podman", "machine", "ssh", w.machine, "sudo", "install", "-m", "0644", staged, path.Join(directory, name)},
		{"podman", "machine", "ssh", w.machine, "sudo", "rm", "-f", staged},
	}
	for _, command := range commands {
		if output, err := run(ctx, command[0], command[1:]...); err != nil {
			return fmt.Errorf("install engine configuration %s: %w: %s", path.Join(directory, name), err, output)
		}
	}
	return nil
}

func (w *hostWriter) write(ctx context.Context, directory, name, content string) error {
	if err := shellSafe(directory, name); err != nil {
		return err
	}
	if !w.remote() {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create engine configuration directory %s: %w", directory, err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("write engine configuration %s: %w", filepath.Join(directory, name), err)
		}
		return nil
	}
	if w.machine != "" {
		return w.machineWrite(ctx, directory, name, content)
	}
	parent, base := path.Split(directory)
	target := path.Join(registryTrustMount, base)
	output, err := w.engine(ctx, content, parent,
		"mkdir -p '"+target+"' && cat > '"+path.Join(target, name)+"'")
	if err != nil {
		return fmt.Errorf("write engine configuration %s through the engine: %w: %s", path.Join(directory, name), err, output)
	}
	return nil
}

func (w *hostWriter) read(ctx context.Context, directory, name string) (string, error) {
	if err := shellSafe(directory, name); err != nil {
		return "", err
	}
	if !w.remote() {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return "", fmt.Errorf("read engine configuration %s: %w", filepath.Join(directory, name), err)
		}
		return string(content), nil
	}
	if w.machine != "" {
		output, err := runWithStdin(ctx, "", "podman", "machine", "ssh", w.machine, "sudo", "cat", path.Join(directory, name))
		if err != nil {
			return "", fmt.Errorf("read engine configuration %s from the machine: %w: %s", path.Join(directory, name), err, output)
		}
		return string(output), nil
	}
	parent, base := path.Split(directory)
	output, err := w.engine(ctx, "", parent, "cat '"+path.Join(registryTrustMount, base, name)+"'")
	if err != nil {
		return "", fmt.Errorf("read engine configuration %s through the engine: %w: %s", path.Join(directory, name), err, output)
	}
	return output, nil
}

func (w *hostWriter) removeFile(ctx context.Context, directory, name string) error {
	if err := shellSafe(directory, name); err != nil {
		return err
	}
	if !w.remote() {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove engine configuration %s: %w", filepath.Join(directory, name), err)
		}
		return nil
	}
	if w.machine != "" {
		if output, err := run(ctx, "podman", "machine", "ssh", w.machine, "sudo", "rm", "-f", path.Join(directory, name)); err != nil {
			return fmt.Errorf("remove engine configuration %s from the machine: %w: %s", path.Join(directory, name), err, output)
		}
		return nil
	}
	parent, base := path.Split(directory)
	output, err := w.engine(ctx, "", parent, "rm -f '"+path.Join(registryTrustMount, base, name)+"'")
	if err != nil {
		return fmt.Errorf("remove engine configuration %s through the engine: %w: %s", path.Join(directory, name), err, output)
	}
	return nil
}

func (w *hostWriter) removeDirectory(ctx context.Context, directory string) error {
	if err := shellSafe(directory, ""); err != nil {
		return err
	}
	if !w.remote() {
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("remove engine configuration directory %s: %w", directory, err)
		}
		return nil
	}
	if w.machine != "" {
		if output, err := run(ctx, "podman", "machine", "ssh", w.machine, "sudo", "rm", "-rf", directory); err != nil {
			return fmt.Errorf("remove engine configuration directory %s from the machine: %w: %s", directory, err, output)
		}
		return nil
	}
	parent, base := path.Split(directory)
	output, err := w.engine(ctx, "", parent, "rm -rf '"+path.Join(registryTrustMount, base)+"'")
	if err != nil {
		return fmt.Errorf("remove engine configuration directory %s through the engine: %w: %s", directory, err, output)
	}
	return nil
}

// engine runs one shell command in a container the engine creates, with a
// directory of the engine host bind-mounted into it.
func (w *hostWriter) engine(ctx context.Context, stdin, mount, script string) (string, error) {
	output, err := runWithStdin(ctx, stdin, "docker", "run", "--rm", "-i",
		"--security-opt", "label=disable",
		"-v", strings.TrimSuffix(mount, "/")+":"+registryTrustMount,
		"--entrypoint", "sh", w.image, "-c", script)
	return string(output), err
}

// shellSafe rejects any path component that would not survive being quoted
// into the shell command the engine container runs.
func shellSafe(parts ...string) error {
	for _, part := range parts {
		if strings.ContainsAny(part, "'\"$`\\\n") {
			return fmt.Errorf("engine configuration path %q is not addressable", part)
		}
	}
	return nil
}

// currentEngineImage resolves the image of the container this process runs in.
// Podman records the container in the mount source of every file it stages for
// the container, which is the identifier the engine answers `inspect` for; the
// container's hostname is not usable because a host-network container carries
// the engine host's.
func currentEngineImage(ctx context.Context) (string, error) {
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read this container's mount table: %w", err)
	}
	match := containerStoragePath.FindSubmatch(mountinfo)
	if match == nil {
		return "", fmt.Errorf("this container's identifier is not in its mount table, so the engine cannot be asked which image to configure registry trust with")
	}
	container := string(match[1])
	image, err := run(ctx, "docker", "inspect", "--format", "{{.Image}}", container)
	if err != nil {
		return "", fmt.Errorf("inspect this container %s: %w: %s", container, err, image)
	}
	identifier := strings.TrimSpace(string(image))
	if identifier == "" {
		return "", fmt.Errorf("engine reported no image for this container %s", container)
	}
	return identifier, nil
}

// containerStoragePath matches the container-storage directory Podman stages
// per-container files under, e.g.
// /var/lib/containers/storage/overlay-containers/<id>/userdata/hosts.
var containerStoragePath = regexp.MustCompile(`-containers/([0-9a-f]{64})/`)

// runningPodmanMachine names the single running Podman machine whose engine
// the Docker-compatible connection reaches, which is the host whose filesystem
// carries that engine's configuration.
func runningPodmanMachine(ctx context.Context) (string, error) {
	machineOutput, err := run(ctx, "podman", "machine", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("list Podman machines: %w: %s", err, machineOutput)
	}
	var machines []struct {
		Name    string `json:"Name"`
		Running bool   `json:"Running"`
	}
	if err := json.Unmarshal(machineOutput, &machines); err != nil {
		return "", fmt.Errorf("decode Podman machine list: %w: %s", err, machineOutput)
	}
	machine := ""
	for _, candidate := range machines {
		if !candidate.Running {
			continue
		}
		if machine != "" {
			return "", fmt.Errorf("multiple Podman machines are running; the Docker-compatible connection is ambiguous")
		}
		machine = candidate.Name
	}
	if machine == "" {
		return "", fmt.Errorf("container runtime reported Podman Engine but no Podman machine is running")
	}
	return machine, nil
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

	machine, err := runningPodmanMachine(ctx)
	if err != nil {
		return nil, err
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

// runWithStdin runs a command with stdin fed from a string and returns its
// standard output alone, so output that has to match a file's bytes exactly is
// not polluted by anything the engine writes to standard error.
func runWithStdin(parent context.Context, stdin, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}
