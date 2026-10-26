// Package nodeagent is the client side of the node agent: the container that
// boots Firecracker microVMs for the services configured onto it, holding the
// KVM device and the network capability the simulator process never holds.
// The agent's program is cmd/nodeagent, the guest's first process is
// cmd/guestinit, and the types here are their wire contract.
package nodeagent

import "time"

type GuestSpec struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname,omitempty"`
	// Hosts are name:address entries for the guest's hosts file; the address
	// host-gateway stands for the network's gateway, as the engine reads it.
	Hosts []string `json:"hosts,omitempty"`
	// Labels mark the guest for the simulator that placed it, as a
	// container's labels do, so a restarted simulator finds it again.
	Labels  map[string]string `json:"labels,omitempty"`
	Command []string          `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// WorkingDir is where the first process starts; empty is /.
	WorkingDir string `json:"workingDir,omitempty"`
	// User runs the first process as uid[:gid]; empty is root.
	User string `json:"user,omitempty"`
	// Network places the guest on the agent's network at a fixed address.
	Network GuestNetwork `json:"network"`
	// ExtraNetworks are further networks the guest has an interface on,
	// eth1 onwards, each at its address, with no route out through it: a
	// container's further engine networks.
	ExtraNetworks []GuestNetwork `json:"extraNetworks,omitempty"`
	VCPUs         int            `json:"vcpus,omitempty"`
	// MemoryMiB is the guest's memory; zero takes 512.
	MemoryMiB int `json:"memoryMiB,omitempty"`
	// The agent writes Seed files into the root filesystem before it boots.
	Seed []SeedFile `json:"seed,omitempty"`
	// StopGrace is how long the first process gets between SIGTERM and the
	// guest's power-off.
	StopGrace time.Duration `json:"stopGrace,omitempty"`
	// Ingress is what the guest may receive: stages a packet passes in
	// turn, each a rule list whose first match decides, where an accept
	// hands the packet to the next stage and a stage nothing matches drops
	// it. No stages admit everything, as a cloud with no rules attached
	// does; an empty stage admits nothing.
	Ingress [][]PacketRule `json:"ingress,omitempty"`
	// DiskSlots is how many block devices beyond the root the guest can take
	// while it runs, members' root filesystems among them; zero takes four.
	DiskSlots int `json:"diskSlots,omitempty"`
	// Mounts are the file shares guestinit mounts before the first process
	// starts: what a bind mount gives a container.
	Mounts []Mount `json:"mounts,omitempty"`
}

// Mount is a file share the guest mounts over its network at Path: an
// NFSv4 export the simulator serves at Server's port, read-only when the
// bind was.
type Mount struct {
	// Server is host:/export, the address the guest reaches the share at.
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// File names the share's one entry, which guestinit places at Path as a
	// file: a bind of a single file.
	File string `json:"file,omitempty"`
}

// MemberSpec is a process tree the guest runs beside its first process from
// a root filesystem of its own, sharing the guest's network, hostname and
// kernel: a sidecar container's shape. The member starts once the guest
// runs, and the guest powers off when its first process and every member
// have ended.
type MemberSpec struct {
	Name    string            `json:"name"`
	Labels  map[string]string `json:"labels,omitempty"`
	Command []string          `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// WorkingDir is where the member's process starts, inside its root.
	WorkingDir string `json:"workingDir,omitempty"`
	// User runs the process as uid[:gid], resolved in the member's root.
	User string `json:"user,omitempty"`
	// The agent writes Seed files into the member's root filesystem before it
	// mounts.
	Seed []SeedFile `json:"seed,omitempty"`
	// StopGrace is how long the process gets between SIGTERM and SIGKILL.
	StopGrace time.Duration `json:"stopGrace,omitempty"`
	// Mounts are the file shares guestinit mounts into the member's root before
	// its process starts.
	Mounts []Mount `json:"mounts,omitempty"`
}

// InitProcess selects the guest's first process where a member name selects
// a member: in a log read and in an exec. No member may take the name.
const InitProcess = "init"

// NoDefaultRoute is the DefaultRoute of a guest with no route out of its
// network.
const NoDefaultRoute = "none"

// GuestNetwork is the guest's place on an engine network the agent joined.
type GuestNetwork struct {
	// AgentAddress is the agent's own address on the network, which names the
	// interface the guest bridges onto.
	AgentAddress string `json:"agentAddress"`
	// Address is the guest's address in CIDR notation.
	Address string `json:"address"`
	// Gateway is the network's own gateway, where the guest reaches the
	// engine's host and the shares it serves.
	Gateway string `json:"gateway"`
	// DefaultRoute is where the guest's default route goes: empty for
	// Gateway, an address on the network for another next hop, or
	// NoDefaultRoute for none.
	DefaultRoute string `json:"defaultRoute,omitempty"`
	MAC          string `json:"mac"`
	// DNS are the resolvers the guest asks.
	DNS []string `json:"dns,omitempty"`
}

// SeedFile is one file written into the guest's root filesystem.
type SeedFile struct {
	Path string `json:"path"`
	Mode int64  `json:"mode,omitempty"`
	Data []byte `json:"data"`
}

// PacketRule is one rule at the guest's tap, as netrules.PacketRule spells
// it.
type PacketRule struct {
	Protocol   string `json:"protocol"`
	SourceCIDR string `json:"sourceCIDR"`
	FromPort   int    `json:"fromPort"`
	ToPort     int    `json:"toPort"`
	Action     string `json:"action"`
}

// GuestStatus is what the agent reports about a guest.
type GuestStatus string

const (
	// GuestCreating is a guest whose root filesystem is being prepared.
	GuestCreating GuestStatus = "creating"
	// GuestRunning is a guest whose first process runs.
	GuestRunning GuestStatus = "running"
	// GuestExited is a guest whose first process has ended and whose machine
	// has powered off.
	GuestExited GuestStatus = "exited"
)

// GuestState is a guest as the agent sees it. Status is the machine's;
// ExitedAt is when the first process ended, zero while it runs, and the
// machine runs on past it while members do.
type GuestState struct {
	ID      string            `json:"id"`
	Status  GuestStatus       `json:"status"`
	Labels  map[string]string `json:"labels,omitempty"`
	Address string            `json:"address"`
	// Gateway is the guest's network's gateway, where the guest reaches a
	// share the simulator serves, and AgentAddress the agent's own address on
	// that network, where it reaches a share served from the agent's namespace.
	Gateway      string        `json:"gateway,omitempty"`
	AgentAddress string        `json:"agentAddress,omitempty"`
	ExitCode     int           `json:"exitCode"`
	Error        string        `json:"error,omitempty"`
	StartedAt    time.Time     `json:"startedAt"`
	ExitedAt     time.Time     `json:"exitedAt,omitempty"`
	StoppedAt    time.Time     `json:"stoppedAt"`
	Members      []MemberState `json:"members,omitempty"`
}

// MemberState is a member as the agent sees it.
type MemberState struct {
	Name      string            `json:"name"`
	Status    GuestStatus       `json:"status"`
	Labels    map[string]string `json:"labels,omitempty"`
	ExitCode  int               `json:"exitCode"`
	Error     string            `json:"error,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	StoppedAt time.Time         `json:"stoppedAt"`
}

// ExecRequest runs a command in a guest: in its first process's root, or in
// the root of the member Process names.
type ExecRequest struct {
	Command []string `json:"command"`
	Process string   `json:"process,omitempty"`
}

// ExecResponse is what the command produced.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// LogLine is one line a guest's process wrote: the first process when
// Member is empty, else the member named.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
	Member string    `json:"member,omitempty"`
}

// GuestConfig is what the agent writes into the guest's root filesystem for
// guestinit: everything the first process needs that the kernel command line
// does not carry.
type GuestConfig struct {
	Hostname   string            `json:"hostname"`
	Address    string            `json:"address"`
	Hosts      []string          `json:"hosts"`
	Command    []string          `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	DNS        []string          `json:"dns"`
	StopGrace  time.Duration     `json:"stopGrace"`
}

// GuestConfigPath is where guestinit reads its configuration.
const GuestConfigPath = "/etc/sockerless/guest.json"

// GuestInitPath is where the agent places guestinit in the root filesystem.
const GuestInitPath = "/sbin/sockerless-init"

// The vsock ports the guest and the agent talk on: the guest serves its
// control API on ControlPort; it reports its first process's exit to the
// agent, which listens on ExitPort.
const (
	ControlPort = 5000
	ExitPort    = 5001
)
