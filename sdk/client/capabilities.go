package client

import (
	"context"
	"slices"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// ServerInfo is the public reply of GET /info.
type ServerInfo struct {
	Name                string   `json:"name"`
	SiteName            string   `json:"site_name"`
	Version             string   `json:"version"`
	RegistrationEnabled bool     `json:"registration_enabled"`
	ChunkSize           int64    `json:"chunk_size"`
	ServerTime          int64    `json:"server_time"`
	Features            []string `json:"features"`
}

// HasFeature reports whether the server advertises feature flag f
// (wire.Feature*, api-v1 §1.3).
func (i *ServerInfo) HasFeature(f string) bool { return i != nil && slices.Contains(i.Features, f) }

// HasFeature reports whether the server advertises feature flag f.
func (m *Me) HasFeature(f string) bool { return m != nil && slices.Contains(m.Features, f) }

// Info returns the unauthenticated server information, including features.
func (c *Client) Info(ctx context.Context) (*ServerInfo, error) {
	var out ServerInfo
	if err := c.do(ctx, "GET", apiPath("/info", nil), nil, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// Capabilities returns the backup capabilities (api-v1 §2).
func (c *Client) Capabilities(ctx context.Context) (*wire.Capabilities, error) {
	var out wire.Capabilities
	if err := c.Call(ctx, "GET", apiPath("/backup/capabilities", nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportBackupState sends the readiness inputs only the device can supply
// (PUT /device/backup-state, api-v1 §11).
func (c *Client) ReportBackupState(ctx context.Context, st wire.BackupState) error {
	return c.Call(ctx, "PUT", apiPath("/device/backup-state", nil), st, nil)
}
