package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// --- restore (api-v1 §7, rev 1.1c) ---

// RestorePlan asks the server to plan a restore (sess). The plan body is
// decoded into plan; resp carries the token to execute it.
func (c *Client) RestorePlan(ctx context.Context, req wire.RestorePlanRequest) (plan *wire.RestorePlan, resp *wire.PlanResponse, err error) {
	var raw struct {
		wire.PlanResponse
		Plan json.RawMessage `json:"plan"`
	}
	if err := c.Call(ctx, "POST", apiPath("/restore/plan", nil), req, &raw); err != nil {
		return nil, nil, err
	}
	var p wire.RestorePlan
	if err := json.Unmarshal(raw.Plan, &p); err != nil {
		return nil, nil, fmt.Errorf("client: restore plan: %w", err)
	}
	out := raw.PlanResponse
	out.Plan = p
	return &p, &out, nil
}

// RestoreExecute executes a restore plan; the restore then waits for its
// approval.
func (c *Client) RestoreExecute(ctx context.Context, planToken string) (*wire.RestoreExecuteResponse, error) {
	var out wire.RestoreExecuteResponse
	if err := c.Call(ctx, "POST", apiPath("/restore/execute", nil), wire.PlanExecuteRequest{PlanToken: planToken}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Restore returns a restore's state (sess).
func (c *Client) Restore(ctx context.Context, id int64) (*wire.Restore, error) {
	var out wire.Restore
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/restore/%d", id), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreItems lists a restore's items, optionally by status (sess).
func (c *Client) RestoreItems(ctx context.Context, id int64, status, cursor string, limit int) (*wire.List[wire.RestoreItem], error) {
	q := listQuery(cursor, limit)
	if status != "" {
		q.Set("status", status)
	}
	var out wire.List[wire.RestoreItem]
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/restore/%d/items", id), q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelRestore cancels a restore, or asks its device to stop (sess).
func (c *Client) CancelRestore(ctx context.Context, id int64) (*wire.Restore, error) {
	var out wire.Restore
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/restore/%d/cancel", id), nil), struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeviceRestore fetches an approved restore for this device (dev).
func (c *Client) DeviceRestore(ctx context.Context, id int64) (*wire.DeviceRestore, error) {
	var out wire.DeviceRestore
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/device/restores/%d", id), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportRestore reports progress and items of a restore this device runs
// (dev). The reply says whether a cancel was requested.
func (c *Client) ReportRestore(ctx context.Context, id int64, p wire.RestoreProgress) (*wire.RestoreProgressReply, error) {
	var out wire.RestoreProgressReply
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/device/restores/%d/progress", id), nil), p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SnapshotDownload starts a zip download of snapshot paths (sess, full
// escrow); poll Operation(id) for the SnapshotDownloadResult.
func (c *Client) SnapshotDownload(ctx context.Context, snapshotID int64, paths []string) (int64, error) {
	var out wire.OperationAccepted
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/snapshots/%d/download", snapshotID), nil), wire.SnapshotDownloadRequest{Paths: paths}, &out); err != nil {
		return 0, err
	}
	return out.OperationID, nil
}

// SnapshotContentPath is the API path of one file's content in a snapshot.
func SnapshotContentPath(snapshotID int64, path string) string {
	return apiPath(fmt.Sprintf("/snapshots/%d/content", snapshotID), url.Values{"path": {path}})
}
