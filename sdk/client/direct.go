package client

// Direct (LAN) uploads (upload.direct). A client may send its upload
// traffic (upload sessions and backup objects) to a local address of the
// server instead of the public URL, for example to bypass a proxy's request
// size, time and speed limits. Everything else keeps using the public URL.
// The local address is used only after VerifyDirect proved it reaches the
// same server; a request that cannot reach it drops the route again
// (DirectLostError) so the caller falls back to the public URL.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

type directRoute struct {
	base *url.URL
	http *http.Client
}

// NormalizeDirectURL validates a user-entered local server address such as
// 192.168.1.20:3000. Plain http is allowed only for addresses on a private
// network (RFC 1918 and link-local IPs, single-label and .local/.lan/.home.arpa
// names); https is allowed for any host.
func NormalizeDirectURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("enter the server's local address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the local address is not valid")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	switch u.Scheme {
	case "https":
	case "http":
		if !privateHost(u.Hostname()) {
			return nil, errors.New("plain http is only allowed for an address on your local network (for example 192.168.1.20:3000); use https:// otherwise")
		}
	default:
		return nil, errors.New("the local address must start with http:// or https://")
	}
	return u, nil
}

func privateHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h != "" && (!strings.Contains(h, ".") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".home.arpa"))
}

// directHTTP is the HTTP client of a direct route: no proxy, no overall
// timeout (large requests on a fast link), but quick to notice an
// unreachable address.
func directHTTP() *http.Client {
	return &http.Client{Transport: &http.Transport{
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}}
}

// SetDirect routes upload traffic to base (nil: back to the public URL).
// Call VerifyDirect first.
func (c *Client) SetDirect(base *url.URL) {
	if base == nil {
		c.direct.Store(nil)
		return
	}
	c.direct.Store(&directRoute{base: base, http: directHTTP()})
}

// Direct returns the local address upload traffic goes to, or nil.
func (c *Client) Direct() *url.URL {
	if d := c.direct.Load(); d != nil {
		return d.base
	}
	return nil
}

// SetOnDirectLost sets fn to be called (once per route) when a request to
// the direct address failed to connect and the route was dropped.
func (c *Client) SetOnDirectLost(fn func(error)) { c.onDirectLost.Store(&fn) }

// DirectLostError reports that a request could not reach the direct
// address; the route has been dropped and a retry uses the public URL.
type DirectLostError struct{ Err error }

func (e *DirectLostError) Error() string {
	return "the server's local address is not reachable: " + e.Err.Error()
}
func (e *DirectLostError) Unwrap() error { return e.Err }

// IsDirectLost reports whether err is a *DirectLostError.
func IsDirectLost(err error) bool {
	var de *DirectLostError
	return errors.As(err, &de)
}

// directPath reports whether a request belongs on the direct route: upload
// sessions and their chunks, and backup object uploads.
func directPath(method, path string) bool {
	if strings.HasPrefix(path, "/api/v1/uploads") {
		return true
	}
	return method == http.MethodPut && strings.HasPrefix(path, "/api/v1/repos/") && strings.Contains(path, "/objects/")
}

// routeFor returns the base URL for a request and the direct route used
// (nil for the public URL).
func (c *Client) routeFor(method, path string) (*url.URL, *directRoute) {
	if d := c.direct.Load(); d != nil && directPath(method, path) {
		return d.base, d
	}
	return c.BaseURL, nil
}

// lost drops route d after a connection failure.
func (c *Client) lost(d *directRoute, err error) error {
	if c.direct.CompareAndSwap(d, nil) {
		if fn := c.onDirectLost.Load(); fn != nil && *fn != nil {
			(*fn)(err)
		}
	}
	return &DirectLostError{Err: err}
}

// VerifyDirect checks that base reaches this same server: it asks the
// public URL for a one-time probe and the local address for its answer. The
// probe (with the device's ResetGen) is returned whenever the public URL
// answered, also when the local address then failed.
func (c *Client) VerifyDirect(ctx context.Context, base *url.URL) (*wire.DirectProbe, error) {
	var probe wire.DirectProbe
	if err := c.Call(ctx, "POST", apiPath("/devices/direct-probe", nil), nil, &probe); err != nil {
		return nil, err
	}
	route := &directRoute{base: base, http: directHTTP()}
	defer route.http.CloseIdleConnections()
	path := apiPath("/devices/direct-probe/"+url.PathEscape(probe.Nonce), nil)
	req, err := c.newRequestAt(context.WithValue(ctx, routeKey{}, route), base, "GET", path, nil, protocol.EmptySHA256, 0, true)
	if err != nil {
		return nil, err
	}
	var ans wire.DirectProbeAnswer
	if err := c.send(req, &ans); err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return &probe, fmt.Errorf("the local address answered but is not this VaultDock server (%s)", ae.Message)
		}
		var de *DirectLostError
		if errors.As(err, &de) {
			err = de.Err
		}
		return &probe, fmt.Errorf("the server's local address is not reachable: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(ans.Answer), []byte(probe.Answer)) != 1 {
		return &probe, errors.New("the local address is a different VaultDock server")
	}
	return &probe, nil
}

// DirectResetGen returns the device's direct-upload reset generation
// through the public URL (a probe that is not completed).
func (c *Client) DirectResetGen(ctx context.Context) (int64, error) {
	var probe wire.DirectProbe
	if err := c.Call(ctx, "POST", apiPath("/devices/direct-probe", nil), nil, &probe); err != nil {
		return 0, err
	}
	return probe.ResetGen, nil
}

// RestartUpload deletes an upload session so the file is uploaded again
// from the start, without recording a cancelled transfer.
func (c *Client) RestartUpload(ctx context.Context, id string) error {
	return c.Call(ctx, "DELETE", apiPath("/uploads/"+url.PathEscape(id), url.Values{"restart": {"1"}}), nil, nil)
}
