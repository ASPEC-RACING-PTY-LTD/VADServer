// Package client is the Go client for the VaultDock API used by the desktop
// application and by integration tests.
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// APIError is a structured error response. Code is the api-v1 error code
// (the wire.Code* constants) and Details the raw error.details object.
type APIError struct {
	Status     int
	Code       string
	Message    string
	Details    json.RawMessage
	RetryAfter time.Duration
	Body       json.RawMessage
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Retryable reports whether the request may succeed if repeated later.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 408, ae.Status == 425, ae.Status == 429, ae.Status >= 500 && ae.Status != 501 && ae.Status != 507:
			return true
		case ae.Status == 401 && (ae.Code == "clock_skew" || ae.Code == "replay"):
			return true
		case ae.Status == 422 && ae.Code == "chunk_hash_mismatch":
			return true
		}
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true // network errors
}

// IsCode reports whether err is an APIError with the given code.
func IsCode(err error, code string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

// Client talks to one VaultDock server.
type Client struct {
	BaseURL  *url.URL
	HTTP     *http.Client
	DeviceID int64
	Key      ed25519.PrivateKey
	// clock offset (server - local) in milliseconds
	offset atomic.Int64
	// UserAgent sent with requests.
	UserAgent string
	// ChunkBody optionally wraps chunk upload bodies (progress counting,
	// bandwidth limiting).
	ChunkBody func(io.Reader) io.Reader
	// direct is the verified local route for upload traffic (direct.go).
	direct       atomic.Pointer[directRoute]
	onDirectLost atomic.Pointer[func(error)]
}

// routeKey marks a request sent over a direct route.
type routeKey struct{}

// Options configure a client.
type Options struct {
	// PinnedCertSHA256 trusts exactly this leaf certificate (hex SHA-256 of
	// its DER encoding) for servers using a self-signed certificate. When
	// empty, the system trust store and hostname verification are used.
	PinnedCertSHA256 string
	Timeout          time.Duration
}

// ErrInsecureURL is returned for plain http URLs to non-local hosts.
var ErrInsecureURL = errors.New("the server URL must use https:// (plain http is only allowed for localhost)")

// NormalizeURL validates a user-entered server URL.
func NormalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("enter the server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the server URL is not valid")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !(host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && ip.IsLoopback())) {
			return nil, ErrInsecureURL
		}
	default:
		return nil, errors.New("the server URL must start with https://")
	}
	return u, nil
}

// New creates a client.
func New(base *url.URL, opt Options) *Client {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if pin := strings.ToLower(strings.ReplaceAll(opt.PinnedCertSHA256, ":", "")); pin != "" {
		// Exact leaf pinning replaces CA validation for self-signed servers;
		// any other certificate is rejected.
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // VerifyConnection enforces the pin
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server presented no certificate")
			}
			if CertFingerprint(cs.PeerCertificates[0]) != pin {
				return &PinMismatchError{Got: CertFingerprint(cs.PeerCertificates[0])}
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       tlsCfg,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
	timeout := opt.Timeout
	return &Client{BaseURL: base, HTTP: &http.Client{Transport: tr, Timeout: timeout}, UserAgent: "VaultDock-Client"}
}

// PinMismatchError is returned when a pinned certificate changed.
type PinMismatchError struct{ Got string }

func (e *PinMismatchError) Error() string {
	return "the server's certificate changed (fingerprint " + FormatFingerprint(e.Got) + "). Reconnect and verify the new fingerprint with your administrator"
}

// CertFingerprint returns the hex SHA-256 of a certificate.
func CertFingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// FormatFingerprint formats a hex fingerprint as AA:BB:...
func FormatFingerprint(h string) string {
	h = strings.ToUpper(strings.ReplaceAll(h, ":", ""))
	var parts []string
	for i := 0; i+1 < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// UntrustedCertError is returned by Probe when the certificate is not
// trusted by the system; the caller may ask the user to verify and pin it.
type UntrustedCertError struct {
	Fingerprint string
	Subject     string
	Reason      string
}

func (e *UntrustedCertError) Error() string {
	return "the server's certificate is not trusted by Windows (" + e.Reason + ")"
}

// Probe checks connectivity and returns server info. If TLS verification
// fails because the certificate is unknown, it returns UntrustedCertError
// with the presented certificate's fingerprint (no data is exchanged over
// the unverified connection).
func Probe(ctx context.Context, base *url.URL, pin string) (map[string]any, error) {
	c := New(base, Options{PinnedCertSHA256: pin, Timeout: 20 * time.Second})
	var info map[string]any
	err := c.do(ctx, "GET", "/api/v1/info", nil, &info, false)
	if err == nil {
		return info, nil
	}
	var uae x509.UnknownAuthorityError
	var cie x509.CertificateInvalidError
	var hne x509.HostnameError
	var tvf *tls.CertificateVerificationError
	if base.Scheme == "https" && pin == "" && (errors.As(err, &uae) || errors.As(err, &cie) || errors.As(err, &hne) || errors.As(err, &tvf)) {
		cert, cerr := fetchCert(ctx, base)
		if cerr != nil {
			return nil, err
		}
		reason := "unknown certificate authority"
		if errors.As(err, &hne) {
			reason = "certificate does not match the host name"
		} else if errors.As(err, &cie) {
			reason = "certificate is invalid or expired"
		}
		return nil, &UntrustedCertError{Fingerprint: CertFingerprint(cert), Subject: cert.Subject.String(), Reason: reason}
	}
	return nil, err
}

// fetchCert performs a TLS handshake only, to read the presented
// certificate for user verification. No HTTP request is sent.
func fetchCert(ctx context.Context, base *url.URL) (*x509.Certificate, error) {
	host := base.Host
	if base.Port() == "" {
		host = net.JoinHostPort(base.Hostname(), "443")
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, ServerName: base.Hostname()}} //nolint:gosec // handshake only, to display the fingerprint
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no certificate")
	}
	return certs[0], nil
}

func (c *Client) now() time.Time {
	return time.Now().Add(time.Duration(c.offset.Load()) * time.Millisecond)
}

func (c *Client) observeServerTime(resp *http.Response, sent, recv time.Time) {
	v := resp.Header.Get(protocol.HeaderServerTime)
	if v == "" {
		return
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return
	}
	mid := sent.Add(recv.Sub(sent) / 2)
	c.offset.Store(ms - mid.UnixMilli())
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, bodyHash string, length int64, sign bool) (*http.Request, error) {
	base, d := c.routeFor(method, path)
	if d != nil {
		ctx = context.WithValue(ctx, routeKey{}, d)
	}
	return c.newRequestAt(ctx, base, method, path, body, bodyHash, length, sign)
}

// newRequestAt builds a request for path below base. The signature covers
// the method, path and body, not the host, so it is valid at any address of
// the server.
func (c *Client) newRequestAt(ctx context.Context, base *url.URL, method, path string, body io.Reader, bodyHash string, length int64, sign bool) (*http.Request, error) {
	u := *base
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	u.Path = base.Path + rel.Path
	u.RawQuery = rel.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = length
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if sign && c.Key != nil {
		h, err := protocol.Sign(c.Key, strconv.FormatInt(c.DeviceID, 10), method, req.URL.RequestURI(), bodyHash, c.now())
		if err != nil {
			return nil, err
		}
		req.Header.Set(protocol.HeaderDevice, h.DeviceID)
		req.Header.Set(protocol.HeaderTimestamp, h.Timestamp)
		req.Header.Set(protocol.HeaderNonce, h.Nonce)
		req.Header.Set(protocol.HeaderBodySHA256, h.BodySHA256)
		req.Header.Set(protocol.HeaderSignature, h.Signature)
	}
	return req, nil
}

// open sends req and returns the response for a status below 400. Error
// responses are closed and returned as *APIError.
func (c *Client) open(req *http.Request) (*http.Response, error) {
	sent := time.Now()
	hc := c.HTTP
	d, _ := req.Context().Value(routeKey{}).(*directRoute)
	if d != nil {
		hc = d.http
	}
	resp, err := hc.Do(req)
	if err != nil {
		if d != nil && req.Context().Err() == nil {
			return nil, c.lost(d, err)
		}
		return nil, err
	}
	c.observeServerTime(resp, sent, time.Now())
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, decodeAPIError(resp, raw)
	}
	return resp, nil
}

// decodeAPIError builds an *APIError from an error response and its body
// (the api-v1 envelope {"error":{"code","message","details"}}).
func decodeAPIError(resp *http.Response, raw []byte) *APIError {
	ae := &APIError{Status: resp.StatusCode, Body: raw}
	var env struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil {
		ae.Code, ae.Message, ae.Details = env.Error.Code, env.Error.Message, env.Error.Details
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil {
			ae.RetryAfter = time.Duration(s) * time.Second
		}
	}
	return ae
}

func (c *Client) send(req *http.Request, out any) error {
	_, err := c.sendStatus(req, out)
	return err
}

// sendStatus is send that also returns the success status code. An empty
// success body leaves out untouched.
func (c *Client) sendStatus(req *http.Request, out any) (int, error) {
	resp, err := c.open(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent || req.Method == http.MethodHead {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any, sign bool) error {
	_, err := c.doStatus(ctx, method, path, in, out, sign, nil)
	return err
}

// doStatus performs a JSON request with optional extra (unsigned) headers
// and returns the success status code.
func (c *Client) doStatus(ctx context.Context, method, path string, in, out any, sign bool, hdr http.Header) (int, error) {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return 0, err
		}
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := c.newRequest(ctx, method, path, rdr, protocol.SHA256Hex(body), int64(len(body)), sign)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return c.sendStatus(req, out)
}

// Call performs a signed JSON request.
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, method, path, in, out, true)
}

// --- enrolment ---

type EnrollRequest struct {
	Login         string `json:"login"`
	Password      string `json:"password"`
	DeviceName    string `json:"device_name"`
	OS            string `json:"os"`
	Identifier    string `json:"identifier"`
	PublicKey     string `json:"public_key"`
	ClientVersion string `json:"client_version"`
	// api-v1 §5A key-exchange fields, required for new clients and omitted
	// by older callers that do not set them.
	BoxPub      string `json:"box_pub,omitempty"`
	InstallMode string `json:"install_mode,omitempty"`
	CoreVersion string `json:"core_version,omitempty"`
}

type EnrollResponse struct {
	RequestID      int64  `json:"request_id"`
	PollToken      string `json:"poll_token"`
	UserCode       string `json:"user_code"`
	ExpiresAt      int64  `json:"expires_at"`
	PollInterval   int    `json:"poll_interval_seconds"`
	KeyFingerprint string `json:"key_fingerprint"`
	Username       string `json:"username"`
}

// Enroll sends credentials once to request device authorisation. The
// password is not stored by the client.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	var out EnrollResponse
	if err := c.do(ctx, "POST", "/api/v1/device/enroll", req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

type EnrollStatus struct {
	Status    string `json:"status"`
	DeviceID  int64  `json:"device_id"`
	ExpiresAt int64  `json:"expires_at"`
	// ServerIdentity is set after approval (api-v1 §5A). The device pins
	// its SIKFingerprint (see CheckSIKPin).
	ServerIdentity *wire.ServerIdentity `json:"server_identity,omitempty"`
}

// EnrollStatus polls the approval state.
func (c *Client) EnrollStatus(ctx context.Context, requestID int64, pollToken string) (*EnrollStatus, error) {
	req, err := c.newRequest(ctx, "GET", fmt.Sprintf("/api/v1/device/enroll/%d", requestID), nil, "", 0, false)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pollToken)
	var out EnrollStatus
	if err := c.send(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- device API ---

type Me struct {
	Device struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"device"`
	User struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		QuotaBytes  int64  `json:"quota_bytes"`
		UsedBytes   int64  `json:"used_bytes"`
	} `json:"user"`
	Server struct {
		Version             string `json:"version"`
		ChunkSize           int64  `json:"chunk_size"`
		MaxFileBytes        int64  `json:"max_file_bytes"`
		MaxConcurrentChunks int    `json:"max_concurrent_chunks"`
		SiteName            string `json:"site_name"`
	} `json:"server"`
	// Features are the api-v1 §1.3 feature flags (wire.Feature*).
	Features []string `json:"features"`
}

func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	return &out, c.Call(ctx, "GET", "/api/v1/device/me", nil, &out)
}

// EnsureFolder creates a folder path (mkdir -p) in the user's storage.
func (c *Client) EnsureFolder(ctx context.Context, path string) error {
	return c.Call(ctx, "POST", "/api/v1/files/ensure-folder", map[string]string{"path": path}, nil)
}

// RemoteAutomation is an automation delivered to the device.
type RemoteAutomation struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	UpdatedAt  int64           `json:"updated_at"`
	Definition json.RawMessage `json:"definition"`
}

func (c *Client) Automations(ctx context.Context) ([]RemoteAutomation, error) {
	var out struct {
		Automations []RemoteAutomation `json:"automations"`
	}
	return out.Automations, c.Call(ctx, "GET", "/api/v1/device/automations", nil, &out)
}

func (c *Client) ReportState(ctx context.Context, state any) error {
	return c.Call(ctx, "PUT", "/api/v1/device/state", state, nil)
}

type TransferReport struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

func (c *Client) ReportTransfers(ctx context.Context, items []TransferReport) error {
	return c.Call(ctx, "POST", "/api/v1/device/transfers", map[string]any{"transfers": items}, nil)
}

type NotificationReport struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   string `json:"level"`
}

func (c *Client) Notify(ctx context.Context, items []NotificationReport) error {
	return c.Call(ctx, "POST", "/api/v1/device/notifications", map[string]any{"notifications": items}, nil)
}

// --- response helpers ---

// apiPath joins the /api/v1 prefix, a path and an optional query.
func apiPath(path string, q url.Values) string {
	p := "/api/v1" + path
	if enc := q.Encode(); enc != "" {
		p += "?" + enc
	}
	return p
}

// listQuery returns the api-v1 list parameters (cursor, limit) as a query.
func listQuery(cursor string, limit int) url.Values {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

// Accepted is a decoded 202 reply: a long operation
// (`{"operation_id"}`) or a gated action awaiting approval
// (`{"approval_id","operation_id"|null}`). Exactly one of the two is set.
type Accepted struct {
	Operation *wire.OperationAccepted
	Approval  *wire.ApprovalAccepted
}

// ParseAccepted decodes a 202 body into Accepted.
func ParseAccepted(raw []byte) (*Accepted, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if _, ok := probe["approval_id"]; ok {
		var a wire.ApprovalAccepted
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return &Accepted{Approval: &a}, nil
	}
	if _, ok := probe["operation_id"]; ok {
		var o wire.OperationAccepted
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		return &Accepted{Operation: &o}, nil
	}
	return nil, errors.New("202 reply carries neither operation_id nor approval_id")
}

// CallAccepted performs a signed JSON request that answers 202 with an
// operation or approval reference.
func (c *Client) CallAccepted(ctx context.Context, method, path string, in any) (*Accepted, error) {
	var raw json.RawMessage
	st, err := c.doStatus(ctx, method, path, in, &raw, true, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusAccepted {
		return nil, fmt.Errorf("expected 202 Accepted, got HTTP %d", st)
	}
	return ParseAccepted(raw)
}

// Operation returns a long operation this device created (api-v1 §3).
func (c *Client) Operation(ctx context.Context, id int64) (*wire.Operation, error) {
	var out wire.Operation
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/operations/%d", id), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ModuleUnavailableDetails are the details of 409 module_unavailable
// (api-v1 §1.1, §6A).
type ModuleUnavailableDetails struct {
	Module   string `json:"module"`
	DeviceID int64  `json:"device_id"`
}

// DecodeDetails unmarshals error.details into v. It returns false when the
// error has no details or they do not decode.
func (e *APIError) DecodeDetails(v any) bool {
	if len(e.Details) == 0 || string(e.Details) == "null" {
		return false
	}
	return json.Unmarshal(e.Details, v) == nil
}

// ModuleUnavailable returns the details of a 409 module_unavailable error.
func ModuleUnavailable(err error) (*ModuleUnavailableDetails, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != wire.CodeModuleUnavailable {
		return nil, false
	}
	var d ModuleUnavailableDetails
	ae.DecodeDetails(&d)
	return &d, true
}

// RepoLocked returns the details of a 409 repo_locked error.
func RepoLocked(err error) (*wire.LockedDetails, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != wire.CodeRepoLocked {
		return nil, false
	}
	var d wire.LockedDetails
	ae.DecodeDetails(&d)
	return &d, true
}
