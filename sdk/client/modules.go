package client

import (
	"context"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// ReportModules sends the device's install mode and module states
// (PUT /device/modules, api-v1 §6A): at enrolment, on change and daily.
func (c *Client) ReportModules(ctx context.Context, rep wire.ModuleReport) error {
	if rep.Modules == nil {
		rep.Modules = []wire.ModuleStatus{}
	}
	return c.Call(ctx, "PUT", apiPath("/device/modules", nil), rep, nil)
}

// ModuleCatalog returns the module catalog (a bare JSON array).
func (c *Client) ModuleCatalog(ctx context.Context) ([]wire.ModuleCatalogEntry, error) {
	var out []wire.ModuleCatalogEntry
	if err := c.Call(ctx, "GET", apiPath("/modules/catalog", nil), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
