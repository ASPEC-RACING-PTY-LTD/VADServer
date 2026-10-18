package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// --- jobs (api-v1 §6; a device sees and edits only its own jobs) ---

// Jobs lists this device's backup jobs.
func (c *Client) Jobs(ctx context.Context, cursor string, limit int) (*wire.List[wire.Job], error) {
	q := listQuery(cursor, limit)
	if c.DeviceID != 0 {
		q.Set("device", strconv.FormatInt(c.DeviceID, 10))
	}
	var out wire.List[wire.Job]
	if err := c.Call(ctx, "GET", apiPath("/backup/jobs", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Job returns one of this device's jobs. The contract has no
// GET /backup/jobs/{id}, so it pages through Jobs.
func (c *Client) Job(ctx context.Context, id int64) (*wire.Job, error) {
	cursor := ""
	for {
		page, err := c.Jobs(ctx, cursor, wire.ListMaxLimit)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			if page.Items[i].ID == id {
				return &page.Items[i], nil
			}
		}
		if page.NextCursor == "" {
			return nil, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("backup job %d not found", id)}
		}
		cursor = page.NextCursor
	}
}

// CreateJob creates a job for this device. DeviceID defaults to the
// client's device. A job needing modules the device lacks returns 409
// module_unavailable; ModuleUnavailable(err) gives the module.
func (c *Client) CreateJob(ctx context.Context, req wire.JobCreateRequest) (*wire.Job, error) {
	if req.DeviceID == 0 {
		req.DeviceID = c.DeviceID
	}
	var out wire.Job
	if err := c.Call(ctx, "POST", apiPath("/backup/jobs", nil), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateJob patches a job, sending If-Match: revision to avoid lost
// updates (409 conflict when the job changed meanwhile).
func (c *Client) UpdateJob(ctx context.Context, id, revision int64, req wire.JobUpdateRequest) (*wire.Job, error) {
	var out wire.Job
	hdr := http.Header{"If-Match": {strconv.FormatInt(revision, 10)}}
	if _, err := c.doStatus(ctx, "PATCH", apiPath(fmt.Sprintf("/backup/jobs/%d", id), nil), req, &out, true, hdr); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- runs ---

// StartRun opens a run (idempotent by client_run_uuid) and returns its id.
func (c *Client) StartRun(ctx context.Context, jobID int64, req wire.RunStartRequest) (int64, error) {
	var out wire.RunStartResponse
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/backup/jobs/%d/runs", jobID), nil), req, &out); err != nil {
		return 0, err
	}
	return out.RunID, nil
}

// RunHeartbeat reports run progress. Callers send at most one per 2 s.
func (c *Client) RunHeartbeat(ctx context.Context, runID int64, p wire.RunProgress) error {
	return c.Call(ctx, "PATCH", apiPath(fmt.Sprintf("/backup/runs/%d", runID), nil), p, nil)
}

// FinishRun sends the final progress with a terminal status (completed,
// partial, failed or cancelled).
func (c *Client) FinishRun(ctx context.Context, runID int64, p wire.RunProgress) error {
	switch p.Status {
	case wire.RunStatusCompleted, wire.RunStatusPartial, wire.RunStatusFailed, wire.RunStatusCancelled:
	default:
		return fmt.Errorf("run status %q is not terminal", p.Status)
	}
	return c.RunHeartbeat(ctx, runID, p)
}

// ReportIssues uploads run issues in batches of at most wire.IssueBatchMax.
func (c *Client) ReportIssues(ctx context.Context, runID int64, issues []wire.IssueReport) error {
	for len(issues) > 0 {
		n := min(len(issues), wire.IssueBatchMax)
		if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/backup/runs/%d/issues", runID), nil), wire.IssueBatchRequest{Issues: issues[:n]}, nil); err != nil {
			return err
		}
		issues = issues[n:]
	}
	return nil
}

// RunIssues lists one page of a run's issues (filter: class, resolution).
func (c *Client) RunIssues(ctx context.Context, runID int64, class, cursor string, limit int) (*wire.List[wire.Issue], error) {
	q := url.Values{}
	if class != "" {
		q.Set("class", class)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out wire.List[wire.Issue]
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/backup/runs/%d/issues", runID), q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SkipIssues marks a run's issues skipped, by ids or by class (api-v1 §6).
func (c *Client) SkipIssues(ctx context.Context, runID int64, req wire.IssueSkipRequest) (*wire.BulkResult, error) {
	var out wire.BulkResult
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/backup/runs/%d/issues/skip", runID), nil), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetrySkipped asks the server to queue a retry_skipped task for a run.
// The contract marks this route sess; a server that keeps it session-only
// rejects the device signature (see the P1-09 report).
func (c *Client) RetrySkipped(ctx context.Context, runID int64) (*wire.DeviceTaskCreated, error) {
	var out wire.DeviceTaskCreated
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/backup/runs/%d/retry-skipped", runID), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
