// Package egress is what an egress sidecar and the simulator that started it
// share: the report the sidecar sends about the connections it carried, and
// the names its configuration travels under.
package egress

import "time"

// ReportPath is where the simulator takes a sidecar's reports, under the
// token the simulator minted for the sidecar.
const ReportPath = "/sockerless/sidecar/egress"

// Environment variable names the sidecar reads its egress configuration from.
const (
	// EnvSources lists the ranges whose connections the sidecar carries,
	// comma-separated CIDRs.
	EnvSources = "SIDECAR_EGRESS_SOURCES"
	// EnvLocal lists the ranges a connection stays inside the network for,
	// which the sidecar leaves alone.
	EnvLocal = "SIDECAR_EGRESS_LOCAL"
	// EnvTCPIdle and EnvUDPIdle are the gateway's idle timeouts in seconds.
	EnvTCPIdle = "SIDECAR_EGRESS_TCP_IDLE"
	EnvUDPIdle = "SIDECAR_EGRESS_UDP_IDLE"
	// EnvGateway names the gateway the sidecar stands for, which its reports
	// carry.
	EnvGateway = "SIDECAR_EGRESS_GATEWAY"
)

// Kind is what an event records.
type Kind string

const (
	// Opened is a connection a source started through the gateway.
	Opened Kind = "opened"
	// Established is a connection the destination accepted.
	Established Kind = "established"
	// Closed is a connection that ended, with what it carried.
	Closed Kind = "closed"
	// Active is a count of the connections open at a moment.
	Active Kind = "active"
)

// Reasons a connection closed.
const (
	ReasonClosed      = "closed"
	ReasonIdle        = "idle"
	ReasonUnreachable = "unreachable"
)

// Event is one thing that happened to a connection through the gateway.
type Event struct {
	Kind            Kind   `json:"kind"`
	Protocol        string `json:"protocol,omitempty"`
	Source          string `json:"source,omitempty"`
	SourcePort      int    `json:"sourcePort,omitempty"`
	Destination     string `json:"destination,omitempty"`
	DestinationPort int    `json:"destinationPort,omitempty"`
	// ViaPort is the gateway's own port the connection left from, on an
	// Established event.
	ViaPort int `json:"viaPort,omitempty"`
	// BytesOut went toward the destination, BytesIn came back from it.
	BytesOut int64  `json:"bytesOut,omitempty"`
	BytesIn  int64  `json:"bytesIn,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Active is the number of open connections, on an Active event.
	Active int       `json:"active,omitempty"`
	At     time.Time `json:"at"`
}

// Report is a batch of events from one gateway's sidecar.
type Report struct {
	Gateway string  `json:"gateway"`
	Events  []Event `json:"events"`
}
