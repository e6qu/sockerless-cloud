// Package nodeagent is the client side of the node agent: the container that
// boots Firecracker microVMs for the services configured onto it, holding the
// KVM device and the network capability the simulator process never holds.
// The agent's program is cmd/nodeagent, the guest's first process is
// cmd/guestinit, and the types here are their wire contract.
package nodeagent

import "time"

// GuestSpec describes one guest.
type GuestSpec struct {
	ID       string            `json:"id"`
	Hostname string            `json:"hostname,omitempty"`
	Command  []string          `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	// WorkingDir is where the first process starts; empty is /.
	WorkingDir string `json:"workingDir,omitempty"`
	// User runs the first process as uid[:gid]; empty is root.
	User string `json:"user,omitempty"`
	// Network places the guest on the agent's network at a fixed address.
	Network GuestNetwork `json:"network"`
	VCPUs   int          `json:"vcpus,omitempty"`
	// MemoryMiB is the guest's memory; zero takes 512.
	MemoryMiB int `json:"memoryMiB,omitempty"`
	// Seed files are written into the root filesystem before it boots.
	Seed []SeedFile `json:"seed,omitempty"`
	// StopGrace is how long the first process gets between SIGTERM and the
	// guest's power-off.
	StopGrace time.Duration `json:"stopGrace,omitempty"`
	// Ingress is what the guest may receive, evaluated first match; an empty
	// list admits everything, as a cloud with no rules attached does not, so
	// a cloud that enforces rules always sends at least a final drop.
	Ingress []PacketRule `json:"ingress,omitempty"`
	// DiskSlots is how many block devices beyond the root the guest can take
	// while it runs; zero takes four.
	DiskSlots int `json:"diskSlots,omitempty"`
}

// GuestNetwork is the guest's place on an engine network the agent joined.
type GuestNetwork struct {
	// AgentAddress is the agent's own address on the network, which names the
	// interface the guest bridges onto.
	AgentAddress string `json:"agentAddress"`
	// Address is the guest's address in CIDR notation.
	Address string `json:"address"`
	Gateway string `json:"gateway"`
	MAC     string `json:"mac"`
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

// GuestState is a guest as the agent sees it.
type GuestState struct {
	ID        string      `json:"id"`
	Status    GuestStatus `json:"status"`
	Address   string      `json:"address"`
	ExitCode  int         `json:"exitCode"`
	Error     string      `json:"error,omitempty"`
	StartedAt time.Time   `json:"startedAt"`
	StoppedAt time.Time   `json:"stoppedAt"`
}

// ExecRequest runs a command in a guest.
type ExecRequest struct {
	Command []string `json:"command"`
}

// ExecResponse is what the command produced.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// LogLine is one line the guest's first process wrote.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

// GuestConfig is what the agent writes into the guest's root filesystem for
// guestinit: everything the first process needs that the kernel command line
// does not carry.
type GuestConfig struct {
	Hostname   string            `json:"hostname"`
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
