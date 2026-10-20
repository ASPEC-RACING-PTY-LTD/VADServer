package wire

// UploadCheck asks for the outcome of an upload whose finish was deferred
// (upload.deferred_finish). Path, Size and SHA256 describe the file, so a
// session the server no longer knows is checked against the stored file.
type UploadCheck struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Outcomes of an UploadCheck.
const (
	UploadDone       = "done"       // stored and verified
	UploadPending    = "pending"    // still being finished
	UploadFailed     = "failed"     // the finish failed (Error says why)
	UploadIncomplete = "incomplete" // chunks are missing: resume the upload
	UploadMissing    = "missing"    // no session and no matching file: upload again
)

// UploadOutcome is the server's answer for one UploadCheck.
type UploadOutcome struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// UploadResultsRequest is the body of POST /uploads/results.
type UploadResultsRequest struct {
	Uploads []UploadCheck `json:"uploads"`
}

// UploadResultsResponse is its reply, in request order.
type UploadResultsResponse struct {
	Results []UploadOutcome `json:"results"`
}

// UploadResultsMax bounds the checks in one request.
const UploadResultsMax = 500
