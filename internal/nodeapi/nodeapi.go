// Package nodeapi holds the wire types shared by the server and the vad-node
// agent (cmd/vad-node). Both live in this repository and ship together, so
// the types change in one place.
package nodeapi

import "strings"

// AuthScheme is the Authorization scheme of node requests:
//
//	Authorization: Node <id>.<secret>
const AuthScheme = "Node"

// JoinRequest is sent once by a new node (POST /api/v1/node/join).
type JoinRequest struct {
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"` // the machine's own (LAN) address, from the installer
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Port      int64  `json:"port"`
	Proxy     string `json:"proxy"` // none, cloudflared, nginx, caddy, other
	PublicURL string `json:"public_url"`
	Version   string `json:"version"`
}

// JoinResponse answers a join request. PollToken authenticates the polls.
type JoinResponse struct {
	ID           int64  `json:"id"`
	PollToken    string `json:"poll_token"`
	ExpiresAt    int64  `json:"expires_at"`
	PollInterval int    `json:"poll_interval"` // seconds
}

// JoinStatus answers GET /api/v1/node/join/{id} (header X-Poll-Token).
// Secret is set exactly once, on the first poll after approval.
type JoinStatus struct {
	Status string `json:"status"` // pending, approved, rejected, expired
	Secret string `json:"secret,omitempty"`
}

// Heartbeat is sent periodically by an approved node (POST /api/v1/node/heartbeat).
type Heartbeat struct {
	Name          string `json:"name"`
	Hostname      string `json:"hostname"`
	IP            string `json:"ip"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Port          int64  `json:"port"`
	PublicURL     string `json:"public_url"`
	DiskTotal     int64  `json:"disk_total"`
	DiskFree      int64  `json:"disk_free"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	LastError     string `json:"last_error,omitempty"`
}

// HeartbeatResponse may ask the node to update itself. The binary comes from
// this server only and must match SHA256.
type HeartbeatResponse struct {
	PollInterval int     `json:"poll_interval"` // seconds until the next heartbeat
	Update       *Update `json:"update,omitempty"`
}

type Update struct {
	Version string `json:"version"`
	URL     string `json:"url"` // path on the server, e.g. /api/v1/node/agent/linux/amd64
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// AgentFile describes one downloadable agent binary.
type AgentFile struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// Manifest answers GET /api/v1/node/agent (public): the agent version this
// server ships and its binaries.
type Manifest struct {
	Version string      `json:"version"`
	Files   []AgentFile `json:"files"`
}

// BinaryName is the file name of the agent for os/arch.
func BinaryName(goos, arch string) string { return "vad-node-" + goos + "-" + arch }

// ValidPlatform accepts the platforms the server builds agents for.
func ValidPlatform(goos, arch string) bool {
	return goos == "linux" && (arch == "amd64" || arch == "arm64")
}

// ValidProxy normalises the reverse-proxy choice.
func ValidProxy(p string) string {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case "none", "cloudflared", "nginx", "caddy", "other":
		return p
	}
	return "none"
}
