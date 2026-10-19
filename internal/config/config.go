// Package config loads server configuration from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Listen address, e.g. ":8080".
	Listen string
	// StateDir holds the database, generated secrets and TLS material.
	StateDir string
	// DefaultStorage is the storage location created on first start.
	DefaultStorage string
	// StorageRoots restricts which directories admins may register as
	// storage locations. Empty means any absolute path.
	StorageRoots []string
	// PublicURL is the externally visible base URL (used in messages only).
	PublicURL string
	// TrustedProxies lists CIDRs whose X-Forwarded-* headers are trusted.
	TrustedProxies []netip.Prefix
	// CookieSecure forces the Secure cookie flag. "auto" decides per request.
	CookieSecure string
	// TLS: provide cert/key, or enable a persisted self-signed certificate.
	TLSCert       string
	TLSKey        string
	TLSSelfSigned bool
	// SetupToken must be supplied to create the first administrator.
	SetupToken string
	// GeoIPDB optionally points at a MaxMind/DB-IP .mmdb city database.
	GeoIPDB string
	// LogLevel: debug, info, warn, error. LogFormat: json or text.
	LogLevel  string
	LogFormat string
	// MaxConcurrentChunkWrites bounds simultaneous chunk writes (server capacity).
	MaxConcurrentChunkWrites int
	// ChunkSize advertised to clients.
	ChunkSize int64
	// UploadSessionTTL is how long an idle upload can be resumed.
	UploadSessionTTL time.Duration
	// DevMode relaxes cookie security for local http development.
	DevMode bool
	// UpdateCheck enables periodic checks for new releases of UpdateRepo
	// ("owner/name" on GitHub). The result is shown in the admin console.
	UpdateCheck bool
	UpdateRepo  string
	// AutoUpdate mirrors the installer's scheduled updates ("on"/"off").
	AutoUpdate string
	// KEKFile is the path of the server key-encryption key file
	// (backup-format-v1 §4 "server custody"). Only the path is configured
	// here; the key is created by the installer, never by Load.
	KEKFile string
	// NodeBinaries is the directory with the vad-node agent binaries
	// (vad-node-linux-amd64, vad-node-linux-arm64) served to storage nodes.
	NodeBinaries string
	// AIAllowPrivateEndpoints lets OpenAI-compatible AI endpoints resolve to
	// loopback, private or link-local addresses (SSRF policy, api-v1 §11).
	AIAllowPrivateEndpoints bool
	// SyncTombstoneDays is how long sync delete tombstones are kept
	// (api-v1 §10.2, which requires at least 90 days).
	SyncTombstoneDays int
}

// MinSyncTombstoneDays is the contract minimum tombstone window (api-v1 §10.2).
const MinSyncTombstoneDays = 90

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int64) (int64, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

// Load reads the configuration.
func Load() (*Config, error) {
	c := &Config{
		Listen:         env("VAULTDOCK_LISTEN", ":8080"),
		StateDir:       env("VAULTDOCK_STATE_DIR", "/state"),
		DefaultStorage: env("VAULTDOCK_DEFAULT_STORAGE", "/storage/default"),
		PublicURL:      strings.TrimRight(env("VAULTDOCK_PUBLIC_URL", ""), "/"),
		CookieSecure:   strings.ToLower(env("VAULTDOCK_COOKIE_SECURE", "auto")),
		TLSCert:        env("VAULTDOCK_TLS_CERT", ""),
		TLSKey:         env("VAULTDOCK_TLS_KEY", ""),
		TLSSelfSigned:  envBool("VAULTDOCK_TLS_SELF_SIGNED", false),
		SetupToken:     env("VAULTDOCK_SETUP_TOKEN", ""),
		GeoIPDB:        env("VAULTDOCK_GEOIP_DB", ""),
		LogLevel:       env("VAULTDOCK_LOG_LEVEL", "info"),
		LogFormat:      env("VAULTDOCK_LOG_FORMAT", "json"),
		DevMode:        envBool("VAULTDOCK_DEV", false),
		UpdateCheck:    envBool("VAULTDOCK_UPDATE_CHECK", true),
		UpdateRepo:     env("VAULTDOCK_UPDATE_REPO", "ASPEC-RACING-PTY-LTD/vad-server"),
		AutoUpdate:     strings.ToLower(env("VAULTDOCK_AUTO_UPDATE", "off")),
		NodeBinaries:   env("VAULTDOCK_NODE_BINARIES", "/usr/local/share/vaultdock/nodes"),

		AIAllowPrivateEndpoints: envBool("VAULTDOCK_AI_ALLOW_PRIVATE_ENDPOINTS", false),
	}
	c.KEKFile = env("VAULTDOCK_KEK_FILE", filepath.Join(c.StateDir, "keys", "kek"))
	for _, r := range strings.Split(env("VAULTDOCK_STORAGE_ROOTS", "/storage"), ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("VAULTDOCK_STORAGE_ROOTS: %w", err)
		}
		c.StorageRoots = append(c.StorageRoots, filepath.Clean(abs))
	}
	for _, p := range strings.Split(env("VAULTDOCK_TRUSTED_PROXIES", ""), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		pref, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("VAULTDOCK_TRUSTED_PROXIES: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pref.Masked())
	}
	n, err := envInt("VAULTDOCK_MAX_CONCURRENT_CHUNKS", 64)
	if err != nil {
		return nil, err
	}
	c.MaxConcurrentChunkWrites = int(max(4, min(n, 1024)))
	cs, err := envInt("VAULTDOCK_CHUNK_SIZE_MB", 8)
	if err != nil {
		return nil, err
	}
	c.ChunkSize = max(1, min(cs, 64)) << 20
	ttl, err := envInt("VAULTDOCK_UPLOAD_TTL_HOURS", 48)
	if err != nil {
		return nil, err
	}
	c.UploadSessionTTL = time.Duration(max(1, ttl)) * time.Hour
	td, err := envInt("VAULTDOCK_SYNC_TOMBSTONE_DAYS", MinSyncTombstoneDays)
	if err != nil {
		return nil, err
	}
	c.SyncTombstoneDays = int(max(MinSyncTombstoneDays, min(td, 3650)))
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, fmt.Errorf("VAULTDOCK_TLS_CERT and VAULTDOCK_TLS_KEY must be set together")
	}
	switch c.CookieSecure {
	case "auto", "true", "false":
	default:
		return nil, fmt.Errorf("VAULTDOCK_COOKIE_SECURE must be auto, true or false")
	}
	return c, nil
}

// AllowedStoragePath reports whether p is inside one of the storage roots.
func (c *Config) AllowedStoragePath(p string) bool {
	if len(c.StorageRoots) == 0 {
		return filepath.IsAbs(p)
	}
	p = filepath.Clean(p)
	for _, r := range c.StorageRoots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
