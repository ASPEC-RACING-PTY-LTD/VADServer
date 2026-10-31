// Package nodeapi holds the wire types shared by the server and the vad-node
// agent (cmd/vad-node). Both live in this repository and ship together, so
// the types change in one place.
package nodeapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

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
	// Pairing is a pairing secret from the server's administration
	// (Storage > Nodes > Pair a remote node): a valid one approves the
	// node straight away.
	Pairing string `json:"pairing,omitempty"`
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
	// TerminalEnabled: the node's owner allows admin terminals (VAD_NODE_TERMINAL=on).
	TerminalEnabled bool `json:"terminal_enabled"`
	// StorageRoots are the folders the owner allows copies in (VAD_NODE_STORAGE_ROOTS).
	StorageRoots []string `json:"storage_roots"`
	// Locations reports the node locations this node has prepared.
	Locations []LocationStatus `json:"locations"`
}

// LocationStatus is the state of one prepared node location.
type LocationStatus struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	DiskTotal int64  `json:"disk_total"`
	DiskFree  int64  `json:"disk_free"`
}

// Job kinds handed to nodes (GET /api/v1/node/jobs/next, long poll).
const (
	JobPrepare  = "prepare"  // create a location folder (Payload.Path)
	JobPut      = "put"      // download data (GET .../data) and store it as the blob's copy
	JobGet      = "get"      // upload the stored copy (PUT .../data)
	JobVerify   = "verify"   // report the SHA-256 of the stored copy
	JobDelete   = "delete"   // remove the stored copy
	JobTerminal = "terminal" // open a terminal session (Payload.Session)
)

// Job is one unit of work for a node.
type Job struct {
	ID      int64      `json:"id"`
	Kind    string     `json:"kind"`
	Payload JobPayload `json:"payload"`
}

type JobPayload struct {
	LocationID int64  `json:"location_id,omitempty"`
	Path       string `json:"path,omitempty"` // prepare: the folder
	UserID     int64  `json:"user_id,omitempty"`
	SHA256     string `json:"sha256,omitempty"` // the blob (plaintext hash; names the copy)
	// Kind is "remote" for a file held only by the node (stored apart from
	// copies; see RemoteFileName). Empty for copies.
	Kind    string `json:"kind,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Session string `json:"session,omitempty"` // terminal
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
}

// JobResult is posted when a job ends (POST /api/v1/node/jobs/{id}/result).
type JobResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	SHA256    string `json:"sha256,omitempty"` // of the stored (encrypted) copy
	Size      int64  `json:"size,omitempty"`
	DiskTotal int64  `json:"disk_total,omitempty"`
	DiskFree  int64  `json:"disk_free,omitempty"`
}

// TerminalInput is what the node receives for an open terminal
// (GET /api/v1/node/terminal/{session}/input, long poll).
type TerminalInput struct {
	Data  []byte `json:"data,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Close bool   `json:"close,omitempty"`
}

// CopyFileName is the name of a blob's copy inside a node location:
// <root>/replicas/<user>/<aa>/<bb>/<sha256>.vdr
func CopyFileName(userID int64, sha string) string {
	return "replicas/" + strconv.FormatInt(userID, 10) + "/" + sha[0:2] + "/" + sha[2:4] + "/" + sha + ".vdr"
}

// KindRemote marks jobs on files held only by the node.
const KindRemote = "remote"

// RemoteFileName is where a node keeps a file it alone holds (encrypted for
// the server's key for this node), relative to its location folder.
func RemoteFileName(userID int64, sha string) string {
	return "remote/" + strconv.FormatInt(userID, 10) + "/" + sha[0:2] + "/" + sha[2:4] + "/" + sha + ".vdr2"
}

// Remote reads: the server fetches byte ranges of a remote file from the
// node's public address (GET RemotePath(...), with a Range header). The
// request is signed with the node secret's SHA-256 (hex), which both sides
// know; the node refuses unsigned or stale (over ReadSkew old) requests.
const (
	HeaderTime = "X-VD-Time"
	HeaderSig  = "X-VD-Sig"
	ReadSkew   = 2 * 60 // seconds
)

// RemotePath is the node URL path of a remote file.
func RemotePath(locationID, userID int64, sha string) string {
	return fmt.Sprintf("/v1/remote/%d/%d/%s", locationID, userID, sha)
}

// SignRead signs a remote read: method, path, Range header and time.
func SignRead(key []byte, method, path, rng, ts string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join([]string{method, path, rng, ts}, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidSHA256 accepts 64 lower-case hex characters.
func ValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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
