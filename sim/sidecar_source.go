package sim

import "embed"

// SidecarSource is what the network sidecar compiles from on the engine: its
// command, the packages of this module it imports, and the module's go.mod and
// go.sum, which pin everything else it needs. The embed lives here because a
// package embeds only files at or below its own directory.
//
//go:embed go.mod go.sum sidecar/cmd/sidecar/*.go sidecar/egress/*.go fileshare/*.go procimage/*.go
var SidecarSource embed.FS
