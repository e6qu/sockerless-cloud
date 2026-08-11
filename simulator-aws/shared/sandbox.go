package simulator

import (
	"path"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// SandboxProfile encodes the security restrictions the real cloud
// platform applies to workload containers. The sim enforces them on
// the local Docker daemon so workloads that "work in the sim" can't
// rely on privileges the real cloud would reject.
//
// Profiles are deliberately MORE restrictive than the cloud where
// the Docker primitive permits a stricter setting cheaply (e.g. cap
// drops): "never higher than the real cloud" is the bar; equal-or-
// stricter is acceptable.
type SandboxProfile struct {
	// Privileged: real clouds NEVER allow privileged containers for
	// workload code (only some platform-managed system containers).
	// Always false here.
	Privileged bool

	// ReadonlyRootfs: Lambda + Cloud Run + Functions Gen2 + ACA + AZF
	// all enforce read-only rootfs for workload containers; only
	// declared writable mounts (e.g. /tmp) are writable.
	ReadonlyRootfs bool

	// User: when non-empty, force this UID:GID (or "name") into
	// container.Config.User. Lambda uses uid 1051 ("sbx_user1051");
	// Cloud Run defaults to non-root if not specified by image.
	User string

	// CapDrop: capabilities to drop. ALL means drop the kernel
	// CAP_BASE set; CapAdd lets specific caps back in.
	CapDrop []string

	// CapAdd: capabilities to keep. Real Lambda workloads have ~zero
	// extra caps. Cloud Run grants SETUID/SETGID by default; ACA
	// similar.
	CapAdd []string

	// NoNewPrivileges: maps to `--security-opt=no-new-privileges`.
	// Hardens setuid binaries against escalation. All clouds enforce.
	NoNewPrivileges bool

	// TmpfsSize: when non-empty, mount /tmp as tmpfs with this size
	// option string ("size=512m"). Lambda enforces tmpfs /tmp.
	TmpfsSize string

	// DenyDockerSocket: refuse to mount the host's docker.sock under
	// any path. Real clouds expose no such surface.
	DenyDockerSocket bool

	// DenyHostNetwork: refuse `NetworkMode=host`. Real clouds expose
	// no host networking to workloads.
	DenyHostNetwork bool
}

// SandboxLambda matches AWS Lambda's container execution environment:
// read-only rootfs except /tmp; sandbox user (uid 1051); no new
// privileges; minimal capabilities.
//
// Source: AWS Lambda Operator Guide — "Lambda runtime environment"
// (https://docs.aws.amazon.com/lambda/latest/operatorguide/runtime-environment.html).
var SandboxLambda = SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   true,
	User:             "1051:1051", // sbx_user1051
	CapDrop:          []string{"ALL"},
	NoNewPrivileges:  true,
	TmpfsSize:        "size=512m", // configurable in real Lambda; sim uses the default
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxFargate matches ECS Fargate's task isolation. Fargate
// permits more flexibility than Lambda (e.g. user from image) but
// still denies host net + docker.sock + privileged.
var SandboxFargate = SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "NET_BIND_SERVICE", "SETFCAP", "SYS_CHROOT"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// Apply mutates the given HostConfig to enforce the profile. Returns
// an error if cfg.NetworkMode or cfg.Binds violates a deny rule
// (these are caller mistakes — not silently fixed).
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

// bindSource extracts the host-side source path from a Docker `-v` bind spec.
// Docker binds are `src:dst[:opts]`; a named volume (`name:dst`) has a source
// that isn't an absolute path, which we ignore (it can't reach a host socket).
// Windows-style `C:\` sources are not relevant on the Linux daemons the sim
// drives.
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

// Sentinel errors so callers can distinguish + tests assert.
var (
	errSandboxHostNet    = sandboxErr("network mode 'host' is denied — no real cloud platform exposes host networking to workloads")
	errSandboxDockerSock = sandboxErr("bind mount of host docker.sock is denied — no real cloud platform exposes the host's container runtime to workloads")
)

type sandboxErr string

func (e sandboxErr) Error() string { return string(e) }
