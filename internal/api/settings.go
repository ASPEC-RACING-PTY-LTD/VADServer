package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Settings are platform settings editable by administrators.
type Settings struct {
	SiteName                string `json:"site_name"`
	RegistrationEnabled     bool   `json:"registration_enabled"`
	DefaultQuotaBytes       int64  `json:"default_quota_bytes"`
	MaxFileBytes            int64  `json:"max_file_bytes"`
	TrashRetentionDays      int    `json:"trash_retention_days"`
	ActivityRetentionDays   int    `json:"activity_retention_days"`
	DeviceRequestTTLMinutes int    `json:"device_request_ttl_minutes"`
	KeepReplacedVersions    bool   `json:"keep_replaced_versions"`
	MinFreeBytes            int64  `json:"min_free_bytes"`
}

func defaultSettings() Settings {
	return Settings{
		SiteName:                "VaultDock",
		RegistrationEnabled:     false,
		DefaultQuotaBytes:       0,
		MaxFileBytes:            0,
		TrashRetentionDays:      30,
		ActivityRetentionDays:   180,
		DeviceRequestTTLMinutes: 15,
		KeepReplacedVersions:    true,
		MinFreeBytes:            1 << 30,
	}
}

func (s Settings) validate() error {
	switch {
	case strings.TrimSpace(s.SiteName) == "" || len(s.SiteName) > 60:
		return fmt.Errorf("site name must be 1-60 characters")
	case s.DefaultQuotaBytes < 0 || s.MaxFileBytes < 0 || s.MinFreeBytes < 0:
		return fmt.Errorf("sizes must not be negative")
	case s.TrashRetentionDays < 0 || s.TrashRetentionDays > 3650:
		return fmt.Errorf("trash retention must be 0-3650 days (0 keeps items until emptied)")
	case s.ActivityRetentionDays < 7 || s.ActivityRetentionDays > 3650:
		return fmt.Errorf("activity retention must be 7-3650 days")
	case s.DeviceRequestTTLMinutes < 2 || s.DeviceRequestTTLMinutes > 1440:
		return fmt.Errorf("device request expiry must be 2-1440 minutes")
	}
	return nil
}

// SettingsCache keeps settings in memory.
type SettingsCache struct {
	mu sync.RWMutex
	v  Settings
	st *store.Store
	// wmu serialises read-merge-write cycles so concurrent PUTs never lose
	// each other's fields.
	wmu sync.Mutex
}

func LoadSettings(ctx context.Context, st *store.Store) (*SettingsCache, error) {
	c := &SettingsCache{st: st, v: defaultSettings()}
	raw, ok, err := st.GetSetting(ctx, "platform")
	if err != nil {
		return nil, err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), &c.v); err != nil {
			return nil, fmt.Errorf("stored settings are corrupt: %w", err)
		}
	}
	return c, nil
}

func (c *SettingsCache) Get() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.v
}

func (c *SettingsCache) Save(ctx context.Context, v Settings) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.save(ctx, v)
}

// Merge applies a partial JSON settings object onto the current settings
// (defect B14): fields absent from body keep their stored values, present
// fields are applied and the result is validated as a whole. Unknown fields
// are rejected (api-v1 §1). Errors are *apiError (bad_json, invalid_settings)
// except for store failures. It returns the merged settings.
func (c *SettingsCache) Merge(ctx context.Context, body []byte) (Settings, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if t := bytes.TrimSpace(body); len(t) == 0 || t[0] != '{' {
		return Settings{}, errf(400, "bad_json", "Invalid request body: expected a JSON object")
	}
	merged := c.Get()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&merged); err != nil {
		return Settings{}, errf(400, "bad_json", "Invalid request body: %v", err)
	}
	if err := c.save(ctx, merged); err != nil {
		if errors.Is(err, errInvalidSettings) {
			return Settings{}, errf(400, "invalid_settings", "%s", errors.Unwrap(err).Error())
		}
		return Settings{}, err
	}
	return merged, nil
}

var errInvalidSettings = errors.New("invalid settings")

type invalidSettingsError struct{ err error }

func (e invalidSettingsError) Error() string        { return e.err.Error() }
func (e invalidSettingsError) Unwrap() error        { return e.err }
func (e invalidSettingsError) Is(target error) bool { return target == errInvalidSettings }

// save persists v; the caller holds wmu.
func (c *SettingsCache) save(ctx context.Context, v Settings) error {
	if err := v.validate(); err != nil {
		return invalidSettingsError{err}
	}
	b, _ := json.Marshal(v)
	if err := c.st.SetSetting(ctx, "platform", string(b)); err != nil {
		return err
	}
	c.mu.Lock()
	c.v = v
	c.mu.Unlock()
	return nil
}

// Geo resolves approximate locations for IPs.
type Geo struct {
	db *maxminddb.Reader
}

func OpenGeo(path string) (*Geo, error) {
	if path == "" {
		return &Geo{}, nil
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &Geo{db: db}, nil
}

// Describe returns a short human description of where an IP is.
func (g *Geo) Describe(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "Unknown"
	}
	switch {
	case a.IsLoopback():
		return "This server (loopback)"
	case a.IsPrivate() || a.IsLinkLocalUnicast():
		return "Local network"
	}
	if g == nil || g.db == nil {
		return "Public internet (no GeoIP database configured)"
	}
	var rec struct {
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
		Country struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
	}
	if err := g.db.Lookup(a).Decode(&rec); err != nil {
		return "Public internet"
	}
	parts := []string{}
	if c := rec.City.Names["en"]; c != "" {
		parts = append(parts, c)
	}
	if c := rec.Country.Names["en"]; c != "" {
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return "Public internet"
	}
	return strings.Join(parts, ", ")
}
