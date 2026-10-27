package sim

import (
	"path"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// SandboxProfile holds the security restrictions the cloud platform applies
// to workload containers, which the simulator enforces on the engine so a
// workload cannot rely on a privilege the cloud would refuse. A profile may
// be stricter than the cloud where the engine makes that cheap (capability
// drops): never laxer than the cloud is the bar.
type SandboxProfile struct {
	// Privileged stays false: the clouds never run workload code privileged,
	// only some platform-managed system containers.
	Privileged bool

	// ReadonlyRootfs mounts the root filesystem read-only, as AWS Lambda, Cloud
	// Run, Cloud Functions (2nd gen), Azure Container Apps and Azure Functions
	// do for workload containers; only declared writable mounts (/tmp) stay
	// writable.
	ReadonlyRootfs bool

	// User, when set, forces this UID:GID (or name) into
	// container.Config.User: AWS Lambda runs as uid 1051 (sbx_user1051); Cloud
	// Run runs as non-root unless the image names a user.
	User string

	// CapDrop lists capabilities to drop; ALL drops the kernel's CAP_BASE set
	// and CapAdd lets specific ones back in.
	CapDrop []string

	// CapAdd lists capabilities to keep. AWS Lambda workloads hold almost none;
	// Cloud Run grants SETUID/SETGID by default, and Azure Container Apps
	// similar.
	CapAdd []string

	// NoNewPrivileges sets `--security-opt=no-new-privileges`, which keeps
	// setuid binaries from escalating; every cloud enforces it.
	NoNewPrivileges bool

	// TmpfsSize, when set, mounts /tmp as tmpfs with this size option
	// ("size=512m"), as AWS Lambda does.
	TmpfsSize string

	// VolatileRun mounts /run as tmpfs, so each boot of a machine starts with
	// an empty /run as a booted Linux system does.
	VolatileRun bool

	// DenyDockerSocket refuses to mount the host's docker.sock under any path;
	// no cloud exposes such a surface.
	DenyDockerSocket bool

	// DenyHostNetwork refuses NetworkMode=host; no cloud gives workloads host
	// networking.
	DenyHostNetwork bool
}

// Apply enforces the profile on hostCfg and containerCfg. It fails when
// hostCfg.NetworkMode or hostCfg.Binds violates a deny rule rather than
// fixing the caller's mistake.
func (p SandboxProfile) Apply(hostCfg *container.HostConfig, containerCfg *container.Config) error {
	if hostCfg == nil {
		return nil
	}
	if p.DenyHostNetwork && string(hostCfg.NetworkMode) == "host" {
		return errSandboxHostNet
	}
	if p.DenyDockerSocket {
		for _, b := range hostCfg.Binds {
			if isDockerSocketBind(b) {
				return errSandboxDockerSock
			}
		}
	}
	hostCfg.Privileged = p.Privileged
	hostCfg.ReadonlyRootfs = p.ReadonlyRootfs
	hostCfg.CapDrop = append(hostCfg.CapDrop, p.CapDrop...)
	hostCfg.CapAdd = append(hostCfg.CapAdd, p.CapAdd...)
	if p.NoNewPrivileges {
		hostCfg.SecurityOpt = append(hostCfg.SecurityOpt, "no-new-privileges")
	}
	if p.TmpfsSize != "" {
		if hostCfg.Tmpfs == nil {
			hostCfg.Tmpfs = map[string]string{}
		}
		if _, exists := hostCfg.Tmpfs["/tmp"]; !exists {
			hostCfg.Tmpfs["/tmp"] = p.TmpfsSize
		}
	}
	if p.VolatileRun {
		if hostCfg.Tmpfs == nil {
			hostCfg.Tmpfs = map[string]string{}
		}
		if _, exists := hostCfg.Tmpfs["/run"]; !exists {
			hostCfg.Tmpfs["/run"] = "mode=755"
		}
	}
	if p.User != "" && containerCfg != nil && containerCfg.User == "" {
		containerCfg.User = p.User
	}
	return nil
}

// dockerSocketPaths are the canonical host paths that expose the container
// runtime's control socket. A bind whose normalized source is one of these —
// OR is an ancestor directory that contains one (e.g. `/var/run` or `/`) — is
// denied, since mounting the parent directory exposes the socket inside it.
var dockerSocketPaths = []string{
	"/var/run/docker.sock",
	"/run/docker.sock",
	"/var/run/podman/podman.sock",
	"/run/podman/podman.sock",
}

// isDockerSocketBind reports whether a Docker bind spec exposes the host's
// container-runtime socket. It parses the bind's source path and normalizes
// `.`/`..`/duplicate-slash segments before matching, so path-traversal variants
// (`/var/run/../run/docker.sock`) and parent-directory mounts (`/var/run`, `/`)
// can't slip a socket through that a naive substring check would miss.
func isDockerSocketBind(bind string) bool {
	src := bindSource(bind)
	if src == "" {
		return false
	}
	// Normalize: collapse `.`/`..`/`//`. path.Clean keeps a leading slash for
	// absolute paths and resolves traversal lexically.
	src = path.Clean(src)
	for _, sock := range dockerSocketPaths {
		if src == sock {
			return true
		}
		// Ancestor-directory mount: `src` is a prefix directory of the socket
		// path, so the socket is reachable inside the mount. Guard the boundary
		// with a trailing slash so `/var/run2` doesn't match `/var/run`.
		if src == "/" || strings.HasPrefix(sock, ensureTrailingSlash(src)) {
			return true
		}
	}
	return false
}

// bindSource extracts the host-side source path from a Docker `-v` bind spec,
// `src:dst[:opts]`. A named volume (`name:dst`) has a source that is not an
// absolute path and cannot reach a host socket, so it yields "". Windows-style
// `C:\` sources do not occur on the Linux daemons the simulator drives.
func bindSource(bind string) string {
	i := strings.IndexByte(bind, ':')
	var src string
	if i < 0 {
		src = bind
	} else {
		src = bind[:i]
	}
	if !strings.HasPrefix(src, "/") {
		return "" // named volume or relative — cannot reference a host socket
	}
	return src
}

func ensureTrailingSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

var (
	errSandboxHostNet    = sandboxErr("network mode 'host' is denied — no real cloud platform exposes host networking to workloads")
	errSandboxDockerSock = sandboxErr("bind mount of host docker.sock is denied — no real cloud platform exposes the host's container runtime to workloads")
)

type sandboxErr string

func (e sandboxErr) Error() string { return string(e) }
