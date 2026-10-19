// Package nodeagent is the vad-node storage agent: it joins a VaultDock
// server (an admin approves the request), keeps the secret the server issues
// once encrypted on this machine, reports a heartbeat and updates itself
// from that server only. cmd/vad-node is its entry point; install.sh
// ("Install Node") installs it as a systemd service.
package nodeagent

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is set at build time (-ldflags -X) to the server version it ships with.
var Version = "dev"

// Config is read from <dir>/.env, written by install.sh.
type Config struct {
	Dir       string // installation directory (holds .env, bin/, data/)
	ServerURL string // VAD_NODE_SERVER_URL
	Name      string // VAD_NODE_NAME
	Port      int64  // VAD_NODE_PORT
	Listen    string // VAD_NODE_LISTEN (host:port of the local status endpoint)
	Proxy     string // VAD_NODE_PROXY: none, cloudflared, nginx, caddy, other
	PublicURL string // VAD_NODE_PUBLIC_URL
	IP        string // VAD_NODE_IP: the machine's address reported at install
	KeyFile   string // VAD_NODE_KEY_FILE: key that encrypts the node secret
	// StorageRoots (VAD_NODE_STORAGE_ROOTS, comma separated): the only
	// folders the server may store copies in; empty = any absolute folder.
	StorageRoots []string
	// Terminal (VAD_NODE_TERMINAL=on) allows the server's superusers to open
	// a terminal here, as TerminalUser (VAD_NODE_TERMINAL_USER, default root).
	Terminal     bool
	TerminalUser string
}

// DefaultKeyFile keeps the encryption key outside the installation folder, so
// a copy or backup of the folder alone does not reveal the node secret.
const DefaultKeyFile = "/etc/vad-node/node.key"

func (c Config) DataDir() string { return filepath.Join(c.Dir, "data") }

// readEnv parses KEY=VALUE lines (no shell expansion; quotes stripped).
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}

// LoadConfig reads <dir>/.env. Environment variables override the file.
func LoadConfig(dir string) (Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	env, err := readEnv(filepath.Join(abs, ".env"))
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", filepath.Join(abs, ".env"), err)
	}
	get := func(k, def string) string {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return v
		}
		if v := env[k]; v != "" {
			return v
		}
		return def
	}
	c := Config{
		Dir:       abs,
		ServerURL: strings.TrimRight(get("VAD_NODE_SERVER_URL", ""), "/"),
		Name:      get("VAD_NODE_NAME", ""),
		Proxy:     get("VAD_NODE_PROXY", "none"),
		PublicURL: get("VAD_NODE_PUBLIC_URL", ""),
		IP:        get("VAD_NODE_IP", ""),
		KeyFile:   get("VAD_NODE_KEY_FILE", DefaultKeyFile),
	}
	c.Port, _ = strconv.ParseInt(get("VAD_NODE_PORT", "0"), 10, 64)
	for _, r := range strings.FieldsFunc(get("VAD_NODE_STORAGE_ROOTS", ""), func(r rune) bool { return r == ',' || r == ' ' }) {
		if filepath.IsAbs(r) {
			c.StorageRoots = append(c.StorageRoots, filepath.Clean(r))
		}
	}
	c.Terminal = strings.EqualFold(get("VAD_NODE_TERMINAL", "off"), "on")
	c.TerminalUser = get("VAD_NODE_TERMINAL_USER", "root")
	c.Listen = get("VAD_NODE_LISTEN", "")
	if c.Listen == "" && c.Port > 0 {
		c.Listen = fmt.Sprintf("0.0.0.0:%d", c.Port)
	}
	if c.Name == "" {
		c.Name, _ = os.Hostname()
	}
	u, err := url.Parse(c.ServerURL)
	if c.ServerURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Config{}, fmt.Errorf("VAD_NODE_SERVER_URL must be the server's address, for example https://vaultdock.example.com")
	}
	return c, nil
}
