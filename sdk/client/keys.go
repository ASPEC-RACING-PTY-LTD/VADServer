package client

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// ServerIdentity returns the server's current identity (GET /keys/server,
// api-v1 §5A). It does not check the pin; use VerifyServerIdentity.
func (c *Client) ServerIdentity(ctx context.Context) (*wire.ServerIdentity, error) {
	var out wire.ServerIdentityResponse
	if err := c.Call(ctx, "GET", apiPath("/keys/server", nil), nil, &out); err != nil {
		return nil, err
	}
	return &out.ServerIdentity, nil
}

// VerifyServerIdentity fetches the server identity and checks it against
// the fingerprint pinned at enrolment. A mismatch returns a 409
// sik_mismatch *APIError: the caller must stop backup until the user
// re-pins with an admin's confirmation.
func (c *Client) VerifyServerIdentity(ctx context.Context, pinned string) (*wire.ServerIdentity, error) {
	id, err := c.ServerIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if err := CheckSIKPin(pinned, id); err != nil {
		return nil, err
	}
	return id, nil
}

// ErrNoSIKPin is returned by CheckSIKPin when no fingerprint is pinned.
var ErrNoSIKPin = errors.New("no server identity fingerprint is pinned for this device")

// CheckSIKPin compares the pinned SIK fingerprint with the server's, ignoring
// case and ':' separators, in constant time. It returns a 409 sik_mismatch
// *APIError when they differ.
func CheckSIKPin(pinned string, id *wire.ServerIdentity) error {
	p := normFingerprint(pinned)
	if p == "" {
		return ErrNoSIKPin
	}
	got := ""
	if id != nil {
		got = normFingerprint(id.SIKFingerprint)
	}
	if subtle.ConstantTimeCompare([]byte(p), []byte(got)) == 1 {
		return nil
	}
	return &APIError{
		Status:  409,
		Code:    wire.CodeSIKMismatch,
		Message: "the server's identity key does not match the one pinned at enrolment; backup is stopped until it is re-pinned with an administrator's confirmation",
	}
}

func normFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s)))
}

// KeyStatus returns the key recipients of a repository (api-v1 §5A).
func (c *Client) KeyStatus(ctx context.Context, repoID int64) (*wire.KeyStatus, error) {
	var out wire.KeyStatus
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/repos/%d/keys/status", repoID), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KeyTaskResult is the result a device reports for a key_share or rewrap
// task. The contract does not define it; see the P1-09 report.
type KeyTaskResult struct {
	RepoID     int64  `json:"repo_id"`
	Epoch      int64  `json:"epoch,omitempty"`
	KeyFileOID string `json:"key_file_oid,omitempty"`
	Error      string `json:"error,omitempty"`
}

// FinishKeyShare reports a key_share task: done when res.Error is empty,
// otherwise failed.
func (c *Client) FinishKeyShare(ctx context.Context, taskID int64, res KeyTaskResult) error {
	return c.finishKeyTask(ctx, taskID, res)
}

// FinishRewrap reports a rewrap task: done when res.Error is empty,
// otherwise failed.
func (c *Client) FinishRewrap(ctx context.Context, taskID int64, res KeyTaskResult) error {
	return c.finishKeyTask(ctx, taskID, res)
}

func (c *Client) finishKeyTask(ctx context.Context, taskID int64, res KeyTaskResult) error {
	status := wire.TaskStatusDone
	if res.Error != "" {
		status = wire.TaskStatusFailed
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return c.TaskResult(ctx, taskID, status, raw)
}
