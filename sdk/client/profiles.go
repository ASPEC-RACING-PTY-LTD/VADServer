package client

import (
	"context"
	"net/url"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// ReportProfiles sends the agent's full Windows profile and consent state
// (PUT /device/profiles, api-v1 §6B). Consent records carry only the
// proof digest, never the DPAPI blob.
func (c *Client) ReportProfiles(ctx context.Context, rep wire.ProfileReport) error {
	if rep.Profiles == nil {
		rep.Profiles = []wire.ProfileReportEntry{}
	}
	if rep.Consents == nil {
		rep.Consents = []wire.ProfileConsentRecord{}
	}
	return c.Call(ctx, "PUT", apiPath("/device/profiles", nil), rep, nil)
}

// ReportSecurityEvent reports a profile security signal; the server
// blacklists the profile and suspends its jobs.
func (c *Client) ReportSecurityEvent(ctx context.Context, sid string, ev wire.ProfileSecurityEventRequest) error {
	return c.Call(ctx, "POST", apiPath("/device/profiles/"+url.PathEscape(sid)+"/security-event", nil), ev, nil)
}
