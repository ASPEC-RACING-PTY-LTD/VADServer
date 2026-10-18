package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// MaxTaskWait is the longest long-poll the server accepts (api-v1 §6).
const MaxTaskWait = 25 * time.Second

// PollTasks long-polls for device tasks, waiting up to wait (clamped to
// 0..MaxTaskWait). Returning a task claims it: every task in the reply is
// now claimed by this device and must be finished with TaskResult. An empty
// slice means the wait expired.
func (c *Client) PollTasks(ctx context.Context, wait time.Duration) ([]wire.DeviceTask, error) {
	secs := int(min(max(wait, 0), MaxTaskWait) / time.Second)
	q := url.Values{"wait": {strconv.Itoa(secs)}}
	var out wire.DeviceTaskPollResponse
	if err := c.Call(ctx, "GET", apiPath("/device/tasks", q), nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// TaskResult finishes a claimed task with status wire.TaskStatusDone or
// wire.TaskStatusFailed. result may be nil (sent as {}).
func (c *Client) TaskResult(ctx context.Context, taskID int64, status string, result json.RawMessage) error {
	if status != wire.TaskStatusDone && status != wire.TaskStatusFailed {
		return fmt.Errorf("task result status %q must be done or failed", status)
	}
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	return c.Call(ctx, "POST", apiPath(fmt.Sprintf("/device/tasks/%d/result", taskID), nil), wire.DeviceTaskResultRequest{Status: status, Result: result}, nil)
}
