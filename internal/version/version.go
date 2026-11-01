// Package version holds the release version, the one place every binary,
// /health and the MCP handshake read it from. Release builds may override it:
//
//	go build -ldflags "-X github.com/nexusriot/omegagrid-agent-go/internal/version.Version=0.6.1" ./cmd/gateway
package version

// Version is the release version (semantic versioning, no "v" prefix, the
// same spelling as the git tags).
var Version = "0.6.0"
