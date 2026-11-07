// Package gwlb is what a Gateway Load Balancer node sidecar and the simulator
// that starts it share: the environment that places the node, and the state
// it long-polls the simulator for.
package gwlb

// StatePath is where a node asks the simulator for its state. The query's
// version names the state the node holds; the simulator answers once the
// state differs from it, or after a while with the same state.
const StatePath = "/sockerless/sidecar/gwlb"

// TokenNetwork is the name a node's token is trusted under: the node, not a
// network, is what its requests come from.
func TokenNetwork(node string) string { return "gwlb:" + node }

const (
	// EnvNode names the node, as its state requests do.
	EnvNode = "SIDECAR_GWLB_NODE"
	// EnvListen is the load balancer's address on its own subnet, where the
	// node exchanges GENEVE with the appliances.
	EnvListen = "SIDECAR_GWLB_LISTEN"
	// EnvEndpoints lists the node's endpoints as <vpce-id>=<address>/<bits>,
	// comma-separated: each endpoint's address with the prefix length of the
	// network it sits on.
	EnvEndpoints = "SIDECAR_GWLB_ENDPOINTS"
)

// Port is the UDP port GENEVE runs on between a Gateway Load Balancer and its
// appliances.
const Port = 6081

// The flow stickiness a target group names.
const (
	FiveTuple           = "5_tuple"
	SourceIPDestIP      = "source_ip_dest_ip"
	SourceIPDestIPProto = "source_ip_dest_ip_proto"
)

// A Gateway Load Balancer closes a flow idle for 350 seconds by default.
const DefaultIdleTimeoutSeconds = 350

// StateWaitSeconds bounds how long the simulator holds a state request whose
// state has not changed.
const StateWaitSeconds = 25

// ServingPrefix starts the line a node prints once it carries traffic.
const ServingPrefix = "sidecar: gwlb serving "

// The GENEVE option class AWS assigns Gateway Load Balancer and the option
// types in it.
const (
	OptionClass      = 0x0108
	OptionEndpoint   = 1
	OptionAttachment = 2
	OptionFlowCookie = 3
)

// Target is one appliance the node may send flows to.
type Target struct {
	Address string `json:"address"`
	// Healthy targets take new flows; a flow already on a target stays on it
	// unless Rebalance says otherwise.
	Healthy bool `json:"healthy"`
}

// Endpoint is what the node needs of one Gateway Load Balancer endpoint at
// run time.
type Endpoint struct {
	ID string `json:"id"`
	// Onward is where the endpoint's subnet routes the traffic the
	// appliances return: a next hop on the endpoint's network, or empty when
	// the subnet's routing names no way out.
	Onward string `json:"onward"`
	// Attachment is the attachment identifier the node sends in GENEVE
	// option type 2.
	Attachment uint64 `json:"attachment"`
	// Sources are the IPv4 ranges of the subnets whose route tables send
	// their traffic through the endpoint, which tell the node which endpoint
	// a packet came through when several share one network.
	Sources []string `json:"sources,omitempty"`
}

// State is everything about a node that changes while it runs.
type State struct {
	Version    string     `json:"version"`
	Stickiness string     `json:"stickiness"`
	Rebalance  bool       `json:"rebalance"`
	IdleTimeS  int        `json:"idleTimeoutSeconds"`
	Targets    []Target   `json:"targets"`
	Endpoints  []Endpoint `json:"endpoints"`
}
